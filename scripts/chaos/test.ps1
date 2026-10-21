$ErrorActionPreference = 'Stop'

if ($env:CHAOS_TEST_DATABASE_URL) {
    $env:TEST_DATABASE_URL = $env:CHAOS_TEST_DATABASE_URL
} else {
    # app.env exports the normal local PostgreSQL URL; chaos tests must always traverse Toxiproxy.
    $env:TEST_DATABASE_URL = 'postgres://zemi:zemi_staging_password@127.0.0.1:15432/zemi_fleet_test?sslmode=disable'
}
if (-not $env:TOXIPROXY_API_URL) {
    $env:TOXIPROXY_API_URL = 'http://127.0.0.1:8474'
}

& go test '-tags=e2e,chaos' '-timeout=90s' '-count=1' '-p' '1' '-run' 'Chaos' './internal/e2e/...'
exit $LASTEXITCODE
