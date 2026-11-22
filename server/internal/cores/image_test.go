package cores

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func testDigest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

func TestCoreImageRepositoryConfiguration(t *testing.T) {
	if got := DefaultCoreImageRepository; got != "ghcr.io/ivanli-cn/token-ne-core" {
		t.Fatalf("default repository: %q", got)
	}
	repo, e := ResolveCoreImageRepository("")
	if e != nil || repo != DefaultCoreImageRepository {
		t.Fatalf("default resolution: %q %v", repo, e)
	}
	repo, e = ResolveCoreImageRepository("  registry.example.test:5000/cores/token-ne  ")
	if e != nil || repo != "registry.example.test:5000/cores/token-ne" {
		t.Fatalf("override resolution: %q %v", repo, e)
	}
	for _, invalid := range []string{
		"ghcr.io/ivanli-cn/token-ne-core:0.2.0",
		"ghcr.io/ivanli-cn/token-ne-core@" + testDigest("a"),
		"Ghcr.io/IvanLi-CN/core",
		"ghcr.io//token-ne-core",
	} {
		if _, e = ResolveCoreImageRepository(invalid); e == nil {
			t.Fatalf("invalid repository accepted: %q", invalid)
		}
	}
}

func TestImageReferenceComputation(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("a")
	ref, e := VersionImageRef(repo, "0.2.0", nil)
	if e != nil || ref != repo+":0.2.0" {
		t.Fatalf("tag reference: %q %v", ref, e)
	}
	ref, e = VersionImageRef(repo, "0.2.0", ptr(digest))
	if e != nil || ref != repo+"@"+digest {
		t.Fatalf("digest reference: %q %v", ref, e)
	}
	for _, tc := range []struct {
		name    string
		repo    string
		version string
		digest  *string
	}{
		{"empty repository", "", "0.2.0", nil},
		{"repository with tag", repo + ":0.2.0", "0.2.0", nil},
		{"repository with digest", repo + "@" + digest, "0.2.0", nil},
		{"latest tag", repo, "latest", nil},
		{"empty version", repo, "", nil},
		{"version with build metadata", repo, "0.2.0+build", nil},
		{"version with slash", repo, "release/0.2.0", nil},
		{"short digest", repo, "0.2.0", ptr("sha256:abcd")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := VersionImageRef(tc.repo, tc.version, tc.digest); e == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestImageReferenceParsingAndRemoteDetection(t *testing.T) {
	digest := testDigest("b")
	for _, tc := range []struct {
		ref       string
		repo      string
		tag       string
		digest    string
		local     bool
		invalid   bool
		canonical string
	}{
		{ref: "token-ne-core:0.1.0", repo: "token-ne-core", tag: "0.1.0", local: true, canonical: "token-ne-core:0.1.0"},
		{ref: "token-ne-core", repo: "token-ne-core", local: true, canonical: "token-ne-core"},
		{ref: "ivanli/token-ne-core:0.1.0", repo: "ivanli/token-ne-core", tag: "0.1.0", canonical: "ivanli/token-ne-core:0.1.0"},
		{ref: "ghcr.io/ivanli-cn/token-ne-core:0.2.0", repo: "ghcr.io/ivanli-cn/token-ne-core", tag: "0.2.0", canonical: "ghcr.io/ivanli-cn/token-ne-core:0.2.0"},
		{ref: "ghcr.io/ivanli-cn/token-ne-core@" + digest, repo: "ghcr.io/ivanli-cn/token-ne-core", digest: digest, canonical: "ghcr.io/ivanli-cn/token-ne-core@" + digest},
		{ref: "localhost:5000/cores/core:1", repo: "localhost:5000/cores/core", tag: "1", canonical: "localhost:5000/cores/core:1"},
		{ref: "registry.test:5000/cores/core:1@" + digest, repo: "registry.test:5000/cores/core", tag: "1", digest: digest, canonical: "registry.test:5000/cores/core:1@" + digest},
		{ref: "", invalid: true},
		{ref: "ghcr.io/ivanli-cn/token-ne-core@" + testDigest("A"), invalid: true},
		{ref: "GHCR.io/ivanli-cn/token-ne-core:1", invalid: true},
		{ref: "ghcr.io/ivanli-cn/token-ne-core:0.2.0:1", invalid: true},
		{ref: "ghcr.io/ivanli-cn/token-ne-core:bad tag", invalid: true},
		{ref: "ghcr.io", invalid: true},
		{ref: "localhost:5000", invalid: true},
		{ref: "sha256:" + strings.Repeat("a", 64), invalid: true},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			ref, e := ParseImageReference(tc.ref)
			if tc.invalid {
				if e == nil {
					t.Fatalf("invalid reference accepted: %#v", ref)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if ref.Repository != tc.repo || ref.Tag != tc.tag || ref.Digest != tc.digest || ref.String() != tc.canonical {
				t.Fatalf("parsed %#v", ref)
			}
			if ref.Local() != tc.local {
				t.Fatalf("local detection for %q: %t", tc.ref, ref.Local())
			}
		})
	}
	if got := (ImageReference{Repository: "ghcr.io/ivanli-cn/token-ne-core", Tag: "1"}).WithDigest(digest).String(); got != "ghcr.io/ivanli-cn/token-ne-core@"+digest {
		t.Fatalf("canonical digest reference: %q", got)
	}
}

func TestDockerPullAndInspectHTTPShape(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("c")
	var query map[string][]string
	var pullAuth, inspectPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/images/create":
			query = r.URL.Query()
			pullAuth = r.Header.Get("Authorization")
			_, _ = io.WriteString(w, `{"status":"Pulling from ivanli-cn/token-ne-core","id":"0.2.0"}`+"\n")
			_, _ = io.WriteString(w, `{"status":"Digest: `+digest+`"}`+"\n")
			_, _ = io.WriteString(w, `{"status":"Status: Downloaded newer image"}`+"\n")
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/images/"):
			inspectPath = r.URL.Path
			_, _ = io.WriteString(w, `{"Id":"sha256:aaa","RepoTags":["`+repo+`:0.2.0"],"RepoDigests":["`+repo+`@`+digest+`"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	engine := &Engine{HTTP: srv.Client(), Base: srv.URL}
	image, e := engine.PullImage(context.Background(), repo+":0.2.0")
	if e != nil {
		t.Fatal(e)
	}
	if got := query["fromImage"]; len(got) != 1 || got[0] != repo {
		t.Fatalf("fromImage query: %#v", query)
	}
	if got := query["tag"]; len(got) != 1 || got[0] != "0.2.0" {
		t.Fatalf("tag query: %#v", query)
	}
	if pullAuth != "" {
		t.Fatalf("pull sent registry credentials: %q", pullAuth)
	}
	if inspectPath != "/images/"+repo+":0.2.0/json" {
		t.Fatalf("inspect path: %q", inspectPath)
	}
	if got, ok := image.RepositoryDigest(repo); !ok || got != digest {
		t.Fatalf("resolved digest: %q %t", got, ok)
	}
	if _, e = engine.InspectImage(context.Background(), repo+"@"+digest); e != nil {
		t.Fatal(e)
	}
	if inspectPath != "/images/"+repo+"@"+digest+"/json" {
		t.Fatalf("digest inspect path: %q", inspectPath)
	}
}

func TestDockerPullFailureAndDigestQuery(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("d")
	var query map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = io.WriteString(w, `{"status":"Pulling"}`+"\n")
		_, _ = io.WriteString(w, `{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}`+"\n")
	}))
	defer srv.Close()
	engine := &Engine{HTTP: srv.Client(), Base: srv.URL}
	if _, e := engine.PullImage(context.Background(), repo+"@"+digest); e == nil || !strings.Contains(e.Error(), "manifest unknown") {
		t.Fatalf("stream error not surfaced: %v", e)
	}
	if got := query["fromImage"]; len(got) != 1 || got[0] != repo+"@"+digest {
		t.Fatalf("digest pull query: %#v", query)
	}
	if _, ok := query["tag"]; ok {
		t.Fatalf("digest pull sent a tag: %#v", query)
	}

	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer status.Close()
	if _, e := (&Engine{HTTP: status.Client(), Base: status.URL}).PullImage(context.Background(), repo+":0.2.0"); e == nil {
		t.Fatal("pull HTTP failure ignored")
	}
	if _, e := (&Engine{HTTP: status.Client(), Base: status.URL}).PullImage(context.Background(), "not a reference"); e == nil {
		t.Fatal("invalid reference pulled")
	}
}

func TestEnsureImagePaths(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("e")
	remote := ImageReference{Repository: repo, Tag: "0.2.0"}
	local := ImageReference{Repository: "token-ne-core", Tag: "0.2.0"}

	pulled := &fakeDocker{image: Image{RepoDigests: []string{repo + "@" + digest}}}
	resolved, e := (&Runtime{Docker: pulled}).ensureImage(context.Background(), remote)
	if e != nil || resolved.String() != repo+"@"+digest || pulled.pulls != 1 {
		t.Fatalf("remote tag: %#v %v pulls=%d", resolved, e, pulled.pulls)
	}

	inspected := &fakeDocker{}
	resolved, e = (&Runtime{Docker: inspected}).ensureImage(context.Background(), local)
	if e != nil || resolved.String() != local.String() || inspected.pulls != 0 || inspected.inspects != 1 {
		t.Fatalf("local tag: %#v %v pulls=%d inspects=%d", resolved, e, inspected.pulls, inspected.inspects)
	}

	if _, e = (&Runtime{Docker: &fakeDocker{inspectErr: errors.New("no such image")}}).ensureImage(context.Background(), local); !errors.Is(e, ErrConflict) || !strings.Contains(e.Error(), "no registry") {
		t.Fatalf("absent local image: %v", e)
	}

	present := &fakeDocker{}
	if resolved, e = (&Runtime{Docker: present}).ensureImage(context.Background(), remote.WithDigest(digest)); e != nil || present.pulls != 0 || present.inspects != 1 {
		t.Fatalf("present digest: %v pulls=%d", e, present.pulls)
	}

	missing := &fakeDocker{inspectFailures: 1}
	if resolved, e = (&Runtime{Docker: missing}).ensureImage(context.Background(), remote.WithDigest(digest)); e != nil || resolved.String() != remote.WithDigest(digest).String() || missing.pulls != 1 || missing.inspects != 2 {
		t.Fatalf("absent digest: %#v %v pulls=%d inspects=%d", resolved, e, missing.pulls, missing.inspects)
	}

	failing := &fakeDocker{pullErr: errors.New("registry unavailable")}
	if _, e = (&Runtime{Docker: failing}).ensureImage(context.Background(), remote); !errors.Is(e, ErrConflict) || !strings.Contains(e.Error(), "registry unavailable") {
		t.Fatalf("pull failure: %v", e)
	}
}

func TestPrepareVersionImagePersistsResolvedDigest(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("f")
	db := &fakeDB{}
	docker := &fakeDocker{image: Image{RepoDigests: []string{repo + "@" + digest}}}
	r := Runtime{Pool: db, Docker: docker, CoreImage: repo}
	image, e := r.prepareVersionImage(context.Background(), versionImageRow{ID: "version-id", Version: "0.2.0", Image: repo + ":0.2.0"})
	if e != nil || image != repo+"@"+digest {
		t.Fatalf("prepared image: %q %v", image, e)
	}
	if !strings.Contains(strings.Join(db.writes, "\n"), "update core_versions set image_ref=$3,image_digest=$2 where id=$1") {
		t.Fatalf("resolved digest not persisted: %v", db.writes)
	}

	stored := digest
	db = &fakeDB{}
	docker = &fakeDocker{image: Image{RepoDigests: []string{repo + "@" + digest}}}
	r = Runtime{Pool: db, Docker: docker, CoreImage: repo}
	if image, e = r.prepareVersionImage(context.Background(), versionImageRow{ID: "version-id", Version: "0.2.0", Image: repo + ":0.2.0", Digest: &stored}); e != nil || image != repo+"@"+digest || docker.pulls != 1 {
		t.Fatalf("tag registration reused digest: %q %v pulls=%d", image, e, docker.pulls)
	}

	db = &fakeDB{}
	r = Runtime{Pool: db, Docker: &fakeDocker{}, CoreImage: repo}
	if image, e = r.prepareVersionImage(context.Background(), versionImageRow{ID: "version-id", Version: "0.2.0", Image: repo + "@" + stored, Digest: &stored, DigestPinned: true}); e != nil || image != repo+"@"+digest {
		t.Fatalf("pinned digest: %q %v", image, e)
	}
	if len(db.writes) != 0 {
		t.Fatalf("unchanged digest rewritten: %v", db.writes)
	}

	r = Runtime{Pool: &fakeDB{}, Docker: &fakeDocker{pullErr: errors.New("denied")}, CoreImage: repo}
	if _, e = r.prepareVersionImage(context.Background(), versionImageRow{ID: "version-id", Version: "0.2.0", Image: repo + ":0.2.0"}); !errors.Is(e, ErrConflict) {
		t.Fatalf("pull failure not reported: %v", e)
	}
}

func TestPullVersionExplicit(t *testing.T) {
	repo := "ghcr.io/ivanli-cn/token-ne-core"
	digest := testDigest("1")
	created := time.Now().UTC()
	row := func(image string, stored *string, pinned bool) func(string, []any) fakeRow {
		return func(string, []any) fakeRow {
			return fakeRow{values: []any{"version-id", "0.2.0", image, stored, pinned, 1, 1, 1, nil, created}}
		}
	}
	db := &fakeDB{row: row(repo+":0.2.0", nil, false)}
	docker := &fakeDocker{image: Image{RepoDigests: []string{repo + "@" + digest}}}
	r := Runtime{Pool: db, Docker: docker, CoreImage: repo}
	version, e := r.PullVersion(context.Background(), uuid.NewString())
	if e != nil {
		t.Fatal(e)
	}
	if docker.pulls != 1 || version.ImageDigest == nil || *version.ImageDigest != digest || version.ImageRef != repo+":0.2.0" {
		t.Fatalf("explicit pull: %#v pulls=%d", version, docker.pulls)
	}
	if !strings.Contains(strings.Join(db.writes, "\n"), "update core_versions set image_ref=$3,image_digest=$2 where id=$1") {
		t.Fatalf("digest not persisted: %v", db.writes)
	}

	localDocker := &fakeDocker{}
	r = Runtime{Pool: &fakeDB{row: row("token-ne-core:0.2.0", nil, false)}, Docker: localDocker, CoreImage: "token-ne-core"}
	if _, e = r.PullVersion(context.Background(), uuid.NewString()); !errors.Is(e, ErrConflict) || localDocker.pulls != 0 {
		t.Fatalf("local repository pull: %v pulls=%d", e, localDocker.pulls)
	}

	r = Runtime{Pool: &fakeDB{row: func(string, []any) fakeRow { return fakeRow{err: pgx.ErrNoRows} }}, Docker: &fakeDocker{}}
	if _, e = r.PullVersion(context.Background(), uuid.NewString()); !errors.Is(e, ErrUnknown) {
		t.Fatalf("unknown version: %v", e)
	}
	if _, e = r.PullVersion(context.Background(), "not-a-uuid"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("invalid version id: %v", e)
	}
}

func TestVersionPullRouteRegistered(t *testing.T) {
	h := HTTP{Token: "control-secret", PublicMeta: BrokerMeta()}
	mux := http.NewServeMux()
	h.Register(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/cores/versions/"+uuid.NewString()+"/pull", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("pull route accepts GET: %d", w.Code)
	}
}

func TestRegisterVersionDerivesImage(t *testing.T) {
	repo := "registry.example.test/cores/token-ne"
	digest := testDigest("2")
	var statement string
	db := &fakeDB{row: func(q string, _ []any) fakeRow {
		statement = q
		return fakeRow{values: []any{"version-id"}}
	}}
	r := Runtime{Pool: db, CoreImage: repo}
	version, e := r.RegisterVersion(context.Background(), Target{Version: "0.3.0", Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema})
	if e != nil {
		t.Fatal(e)
	}
	if version.ImageRef != repo+":0.3.0" || version.ID != "version-id" || version.ImageDigest != nil {
		t.Fatalf("registered version: %#v", version)
	}
	if !strings.Contains(statement, "insert into core_versions") {
		t.Fatalf("registration statement: %q", statement)
	}

	version, e = r.RegisterVersion(context.Background(), Target{Version: "0.3.0", Digest: &digest, Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema})
	if e != nil || version.ImageRef != repo+"@"+digest {
		t.Fatalf("digest registration: %#v %v", version, e)
	}

	for _, input := range []Target{
		{Version: "0.3.0", Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema},
		{Version: "0.3.0", Digest: ptr("sha256:abcd"), Protocol: Protocol, SchemaMin: Schema, SchemaMax: Schema},
		{Version: "0.3.0", Protocol: Protocol + 1, SchemaMin: Schema, SchemaMax: Schema},
	} {
		empty := Runtime{Pool: &fakeDB{}}
		if _, e := empty.RegisterVersion(context.Background(), input); !errors.Is(e, ErrInvalid) {
			t.Fatalf("invalid registration accepted: %#v %v", input, e)
		}
	}
}
