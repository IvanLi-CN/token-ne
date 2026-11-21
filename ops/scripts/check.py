#!/usr/bin/env python3
"""Validate the deployment baseline without contacting Docker or services."""
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def yaml(path):
    # Mike Farah yq v4 is the only YAML dependency; Python uses its JSON output.
    result = subprocess.run(['yq', '-e', '-o=json', '.', str(ROOT / path)],
                            check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def check():
    compose = yaml('ops/compose.yaml')
    services = compose['services']
    expected = {'postgres': {'data'}, 'socket-proxy': {'data'},
                'api': {'edge', 'core-control', 'data'}, 'caddy': {'edge'},
                'core': {'edge', 'core-control'}}
    require(set(services) == set(expected), 'unexpected Compose services')
    require(set(compose['networks']) == {'edge', 'core-control', 'data'}, 'network set changed')
    for name, networks in expected.items():
        service = services[name]
        require(set(service['networks']) == networks, f'{name}: incorrect networks')
        require(not service.get('network_mode') and not service.get('privileged'), f'{name}: unsafe host access')
        if name not in {'api', 'caddy'}:
            require(not service.get('ports'), f'{name}: host ports forbidden')
    require(services['caddy']['ports'] == ['127.0.0.1:${CADDY_HTTP_PORT:-8080}:80'], 'Caddy must bind loopback HTTP only')
    require(services['api']['ports'] == ['127.0.0.1:${METRICS_PORT:-9090}:9090'], 'API may only publish loopback metrics')
    require(services['api']['environment']['CADDY_ADMIN_URL'] == 'unix:///run/caddy/admin.sock', 'API admin must use Unix socket')
    for name, service in services.items():
        mounts = service.get('volumes', [])
        require(not any('docker.sock' in m for m in mounts) or name == 'socket-proxy', 'Docker socket escaped proxy')
        require(not any('caddy_admin:' in m for m in mounts) or name in {'api', 'caddy'}, 'admin socket escaped API/Caddy')
    require(not any('CORE_STATE_HOST_DIR' in m for m in services['api']['volumes']), 'API must not mount core state')

    caddy = json.loads((ROOT / 'ops/caddy/bootstrap.json').read_text())
    require(caddy['admin'] == {'listen': 'unix//run/caddy/admin.sock'}, 'network admin listener forbidden')
    http = caddy['apps']['http']
    require(http['grace_period'] == '0s', 'long streams require unlimited reload grace')
    servers = http['servers']
    require(set(servers) == {'token-ne'}, 'unexpected HTTP servers')
    server = servers['token-ne']
    require(server['listen'] == [':80'], 'bootstrap must serve only HTTP')
    routes = server['routes']
    require([r['@id'] for r in routes] == ['token-ne-deny-internal', 'token-ne-v1', 'token-ne-api', 'token-ne-spa'], 'stable route IDs/order changed')
    require(all(r['terminal'] is True for r in routes), 'routes must terminate')
    require(routes[0]['match'] == [{'path': ['/internal', '/internal/*']}], 'internal paths must be denied first')
    require(routes[0]['handle'] == [{'handler': 'static_response', 'status_code': 404, 'body': 'not found'}], 'internal route must return 404')
    require(routes[1]['match'] == [{'path': ['/v1/*']}], 'v1 matcher changed')
    proxy = routes[1]['handle'][0]
    require(proxy['@id'] == 'token-ne-v1-proxy' and proxy['flush_interval'] == -1, 'stable proxy ID and immediate SSE flush required')
    require(proxy['upstreams'] == [{'dial': 'core:8080'}], 'v1 must target baseline core')
    require(proxy['health_checks']['active']['uri'] == '/readyz', 'core readiness probe required')
    require(routes[2]['match'] == [{'path': ['/api/*', '/healthz', '/readyz']}], 'API paths changed')
    require('match' not in routes[3], 'SPA fallback must be unconditional')
    for route in routes[2:]:
        require(route['handle'] == [{'handler': 'reverse_proxy', 'upstreams': [{'dial': 'api:8080'}]}], 'API/fallback must target API')

    policy = yaml('config/status-rewrite.yaml')
    require(policy == yaml('docs/examples/status-rewrite.yaml'), 'status policy differs from documented example')
    require(policy['schema_version'] == 1 and type(policy['enabled']) is bool, 'invalid policy header')
    protected = {400, 401, 403, 404, 409, 422}
    require(set(policy['never_rewrite']['statuses']) == protected and policy['never_rewrite']['after_commit'] is True, 'policy safety guard changed')
    ids = set()
    for rule in policy['rules']:
        require(rule['id'] not in ids, 'duplicate policy rule ID')
        ids.add(rule['id'])
        match, response = rule['match'], rule['response']
        require(match['committed'] is False and match['retry_exhausted'] is True, 'rewrite only before commit after retry exhaustion')
        require(not protected.intersection(match.get('upstream_status', [])), 'protected status rewritten')
        require(response['status'] == 503 and response['retryable'] is True, 'baseline rewrites must remain retryable 503')

    prom = yaml('ops/prometheus/prometheus.yml')
    example = yaml('ops/prometheus/scrape.example.yml')
    require(example == prom['scrape_configs'], 'Prometheus examples disagree')
    require(len(example) == 1, 'only API is an external scrape target')
    scrape = example[0]
    require(scrape['static_configs'] == [{'targets': ['127.0.0.1:9090']}], 'host Prometheus must use loopback metrics')
    require(scrape['authorization'] == {'type': 'Bearer', 'credentials_file': '/etc/prometheus/token-ne-metrics-token'}, 'metrics bearer token file required')
    dashboard = json.loads((ROOT / 'ops/grafana/dashboards/token-ne-overview.json').read_text())
    require(len(dashboard['panels']) >= 7, 'dashboard panels missing')
    scan_env()
    print('PASS: YAML/JSON, network/port isolation, Caddy routes/admin, policy, monitoring and tracked env placeholders')
    print('NOT RUN: Docker Compose runtime/E2E, live metrics and Grafana rendering')


def scan_env():
    paths = subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0')
    for name in paths:
        if not name or not re.fullmatch(r'\.?env(?:\..+)?', Path(name).name):
            continue
        require(name.endswith('.example'), f'{name}: real env file tracked')
        for line_number, line in enumerate((ROOT / name).read_text().splitlines(), 1):
            match = re.match(r'\s*(?:export\s+)?([A-Z_][A-Z_0-9]*)\s*=\s*(.*?)\s*$', line)
            if not match:
                continue
            key, value = match.groups()
            if re.search(r'PASSWORD|SECRET|TOKEN|(?:^|_)KEY$|CREDENTIAL|DATABASE_URL', key):
                require(value.strip('\"\'') in {'', 'change-me', 'REPLACE_ME'}, f'{name}:{line_number}: non-placeholder secret value (redacted)')


if __name__ == '__main__':
    try:
        check()
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as error:
        print(f'FAIL: {error}', file=sys.stderr)
        sys.exit(1)
