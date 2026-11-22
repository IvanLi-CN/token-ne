package cores

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultCoreImageRepository is the core image repository compiled into the API
// binary. The Dockerfile injects the release repository through -ldflags and
// CORE_IMAGE_REPOSITORY overrides it at runtime.
var DefaultCoreImageRepository = "ghcr.io/ivanli-cn/token-ne-core"

const (
	// CoreImageRepositoryEnv names the runtime override for the repository.
	CoreImageRepositoryEnv = "CORE_IMAGE_REPOSITORY"
	// imagePullTimeout bounds a single registry pull. Deploys and explicit pulls
	// share it so a slow registry cannot pin a request indefinitely.
	imagePullTimeout = 10 * time.Minute
)

// An image repository is a registry host and path without a tag or digest. The
// optional leading group carries the registry host, including a port; the rest is
// the lower-case repository path Docker accepts.
var (
	imageRepositoryPattern = regexp.MustCompile(`^(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	imageTagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	imageDigestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ValidateImageRepository reports whether repo is a bare image repository. A
// registry host must be followed by a repository path, otherwise the reference
// would be read as a Docker Hub repository with a tag.
func ValidateImageRepository(repo string) bool {
	if repo == "" || len(repo) > 255 || !imageRepositoryPattern.MatchString(repo) {
		return false
	}
	host, _, hasPath := strings.Cut(repo, "/")
	return hasPath || !isRegistryHost(host)
}

// isRegistryHost reports whether a repository's first component names a registry
// instead of a Docker Hub namespace.
func isRegistryHost(host string) bool {
	return host == "localhost" || strings.ContainsAny(host, ".:")
}

// ResolveCoreImageRepository validates the configured core image repository and
// falls back to the compiled-in default. A tag or digest is a configuration
// error: the control plane appends them when it derives a version image.
func ResolveCoreImageRepository(override string) (string, error) {
	repo := strings.TrimSpace(override)
	if repo == "" {
		repo = DefaultCoreImageRepository
	}
	if !ValidateImageRepository(repo) {
		return "", fmt.Errorf("%w: %s must be an image repository without a tag or digest", ErrInvalid, CoreImageRepositoryEnv)
	}
	return repo, nil
}

// ImageReference is a parsed container image reference.
type ImageReference struct {
	Repository string
	Tag        string
	Digest     string
}

// ParseImageReference splits ref into repository, tag and digest. Only the
// reference shapes the control plane creates are accepted, which keeps every
// image path free of characters Docker request paths cannot carry.
func ParseImageReference(ref string) (ImageReference, error) {
	if ref == "" || len(ref) > 512 {
		return ImageReference{}, ErrInvalid
	}
	// An image ID is not a repository reference.
	if strings.HasPrefix(ref, "sha256:") {
		return ImageReference{}, ErrInvalid
	}
	name, digest := ref, ""
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name, digest = name[:i], name[i+1:]
		if !imageDigestPattern.MatchString(digest) {
			return ImageReference{}, ErrInvalid
		}
	}
	tag := ""
	if i := strings.LastIndexByte(name, ':'); i >= 0 && !strings.Contains(name[i+1:], "/") {
		name, tag = name[:i], name[i+1:]
		if !imageTagPattern.MatchString(tag) {
			return ImageReference{}, ErrInvalid
		}
	}
	if !ValidateImageRepository(name) {
		return ImageReference{}, ErrInvalid
	}
	return ImageReference{Repository: name, Tag: tag, Digest: digest}, nil
}

func (r ImageReference) String() string {
	ref := r.Repository
	if r.Tag != "" {
		ref += ":" + r.Tag
	}
	if r.Digest != "" {
		ref += "@" + r.Digest
	}
	return ref
}

// WithDigest returns the canonical digest reference for the same repository.
func (r ImageReference) WithDigest(digest string) ImageReference {
	return ImageReference{Repository: r.Repository, Digest: digest}
}

// Local reports whether the reference has no registry host component. Docker can
// only inspect such a repository; there is no registry to pull the image back
// from once it is gone.
func (r ImageReference) Local() bool {
	host, _, hasPath := strings.Cut(r.Repository, "/")
	return !hasPath || !isRegistryHost(host)
}

// VersionImageRef derives the image reference for a registered version. Clients
// never choose the repository: registration supplies a version, and optionally a
// digest that pins the exact image.
func VersionImageRef(repository, version string, digest *string) (string, error) {
	if !ValidateImageRepository(repository) || !imageTagPattern.MatchString(version) || strings.EqualFold(version, "latest") {
		return "", ErrInvalid
	}
	ref := ImageReference{Repository: repository, Tag: version}
	if digest != nil {
		if !imageDigestPattern.MatchString(*digest) {
			return "", ErrInvalid
		}
		ref = ref.WithDigest(*digest)
	}
	return ref.String(), nil
}

// ensureImage makes ref available to Docker and returns the reference to create
// containers with. A digest must already be present locally or be pulled once and
// verified. A remote tag is always pulled so a rollout never starts from a stale
// image, and the pulled digest becomes the container reference. A local
// repository can only be inspected.
func (r *Runtime) ensureImage(ctx context.Context, ref ImageReference) (ImageReference, error) {
	remote := !ref.Local()
	if ref.Digest != "" {
		if _, err := r.Docker.InspectImage(ctx, ref.String()); err == nil {
			return ref, nil
		}
		if !remote {
			return ImageReference{}, fmt.Errorf("%w: image %s is unavailable locally", ErrConflict, ref)
		}
		if _, err := r.Docker.PullImage(ctx, ref.String()); err != nil {
			return ImageReference{}, fmt.Errorf("%w: pull %s: %v", ErrConflict, ref, err)
		}
		if _, err := r.Docker.InspectImage(ctx, ref.String()); err != nil {
			return ImageReference{}, fmt.Errorf("%w: pull %s did not provide the digest", ErrConflict, ref)
		}
		return ref, nil
	}
	if !remote {
		if _, err := r.Docker.InspectImage(ctx, ref.String()); err != nil {
			return ImageReference{}, fmt.Errorf("%w: image %s is not present locally and the repository has no registry", ErrConflict, ref)
		}
		return ref, nil
	}
	image, err := r.Docker.PullImage(ctx, ref.String())
	if err != nil {
		return ImageReference{}, fmt.Errorf("%w: pull %s: %v", ErrConflict, ref, err)
	}
	if digest, ok := image.RepositoryDigest(ref.Repository); ok {
		return ref.WithDigest(digest), nil
	}
	return ref, nil
}

// prepareVersionImage ensures the version image from the configured repository
// is available and returns the reference to create containers with. A tag pull
// resolves the digest and persists it for rollback/reconciliation.
func (r *Runtime) prepareVersionImage(ctx context.Context, v versionImageRow) (string, error) {
	refString, err := VersionImageRef(r.CoreImage, v.Version, v.Digest)
	if err != nil {
		return "", fmt.Errorf("%w: version %q cannot derive an image reference", ErrInvalid, v.Version)
	}
	ref, err := ParseImageReference(refString)
	if err != nil {
		return "", fmt.Errorf("%w: derived image reference %q is invalid", ErrInvalid, refString)
	}
	resolved, err := r.ensureImage(ctx, ref)
	if err != nil {
		return "", err
	}
	if refString != v.Image || resolved.Digest != "" && (v.Digest == nil || *v.Digest != resolved.Digest) {
		if _, err = r.database().Exec(ctx, `update core_versions set image_ref=$3,image_digest=$2 where id=$1`, v.ID, resolved.Digest, refString); err != nil {
			return "", err
		}
	}
	return resolved.String(), nil
}

// PullVersion refreshes a registered version image from the configured
// repository. An explicit pull is how operators restore an image that is no
// longer present without redeploying it.
func (r *Runtime) PullVersion(ctx context.Context, id string) (VersionView, error) {
	if !UUID(id, false) {
		return VersionView{}, ErrInvalid
	}
	var v VersionView
	e := r.database().QueryRow(ctx, `select id::text,version,image_ref,image_digest,protocol_version,config_schema_min,config_schema_max,build_commit,created_at from core_versions where id=$1`, id).
		Scan(&v.ID, &v.Version, &v.ImageRef, &v.ImageDigest, &v.Protocol, &v.SchemaMin, &v.SchemaMax, &v.Commit, &v.Created)
	if errors.Is(e, pgx.ErrNoRows) {
		return VersionView{}, ErrUnknown
	}
	if e != nil {
		return VersionView{}, e
	}
	refString, e := VersionImageRef(r.CoreImage, v.Version, v.ImageDigest)
	if e != nil {
		return VersionView{}, fmt.Errorf("%w: version %q cannot derive an image reference", ErrInvalid, v.Version)
	}
	ref, e := ParseImageReference(refString)
	if e != nil {
		return VersionView{}, fmt.Errorf("%w: derived image reference %q is invalid", ErrInvalid, refString)
	}
	if ref.Local() {
		return VersionView{}, fmt.Errorf("%w: image %s has no registry repository to pull", ErrConflict, ref)
	}
	image, e := r.Docker.PullImage(ctx, ref.String())
	if e != nil {
		return VersionView{}, fmt.Errorf("%w: pull %s: %v", ErrConflict, ref, e)
	}
	digest, ok := image.RepositoryDigest(ref.Repository)
	if !ok {
		digest = ref.Digest
	}
	targetRef, e := VersionImageRef(r.CoreImage, v.Version, v.ImageDigest)
	if e != nil {
		return VersionView{}, e
	}
	if digest != "" {
		v.ImageDigest = &digest
	}
	v.ImageRef = targetRef
	if _, e = r.database().Exec(ctx, `update core_versions set image_ref=$3,image_digest=$2 where id=$1`, id, v.ImageDigest, v.ImageRef); e != nil {
		return VersionView{}, e
	}
	return v, nil
}
