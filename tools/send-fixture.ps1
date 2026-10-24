param(
    [string]$BaseUrl = 'http://127.0.0.1:8088',
    [string]$Key = '0123456789abcdef0123456789abcdef01234567'
)
$fixtureEnd = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds() - 2
$fixtureBody = @{
    application = 'Fixture WordPress'
    host = 'fixture-web-01'
    start = $fixtureEnd - 60
    end = $fixtureEnd
    metrics = @(
        @{name='Framework/WordPress/Plugin/woocommerce';values=@(120,2.4,1.2,0.002,0.12,0.1)},
        @{name='Framework/WordPress/Plugin/contact-form-7';values=@(30,0.6,0.3,0.001,0.08,0.02)},
        @{name='Framework/WordPress/Hook/init';values=@(150,3.0,1.5,0.001,0.12,0.12)}
    )
} | ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri ($BaseUrl.TrimEnd('/') + '/v1/metrics') -Headers @{'X-APM-Ingest-Key'=$Key} -ContentType 'application/json' -Body $fixtureBody
