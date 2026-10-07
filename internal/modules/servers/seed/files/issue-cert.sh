#!/bin/bash
# Issues the first Let's Encrypt certificate with a one-off certbot container in
# standalone mode: nginx can't start without a certificate. Renewals are done by
# the certbot service. Safe to run again: it does nothing when the certificate
# already exists.
set -euo pipefail
cd "$(dirname "$0")"

DOMAIN='{{ .Server.ProxyHostname }}'
# The email parameter is validated by Proxier to hold no quote, $, backtick or backslash.
EMAIL='{{ .Params.letsencrypt_email }}'

docker_cmd() {
    if [ "$(id -u)" -eq 0 ]; then
        docker "$@"
    else
        sudo -n docker "$@"
    fi
}

if [ -s "certbot/conf/live/${DOMAIN}/fullchain.pem" ]; then
    echo "A certificate for ${DOMAIN} already exists."
    exit 0
fi

mkdir -p certbot/conf certbot/www

email_args=(--register-unsafely-without-email)
if [ -n "$EMAIL" ]; then
    email_args=(-m "$EMAIL" --no-eff-email)
fi

echo "Issuing Let's Encrypt certificate for ${DOMAIN}..."

if ! docker_cmd run --rm -p 80:80 \
    -v "$PWD/certbot/conf:/etc/letsencrypt" \
    certbot/certbot certonly --standalone --non-interactive --agree-tos \
    "${email_args[@]}" -d "$DOMAIN"; then
    echo "Failed to issue the certificate. Check that the A record of ${DOMAIN} points to this server and port 80 is open and free."
    exit 1
fi
