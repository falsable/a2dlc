# a2dlc PHP engine. The network is only used at build time; the core runs it with --network none.
# Composer runs in its own stage and installs exactly the versions and hashes of composer.lock; the runtime image has no Composer.
FROM composer:2 AS deps
WORKDIR /engine
COPY composer.json composer.lock ./
RUN composer install --no-dev --no-interaction --no-progress --classmap-authoritative --ignore-platform-req=ext-*

FROM php:8.3-cli
WORKDIR /engine
COPY --from=deps /engine/vendor ./vendor
COPY composer.json composer.lock dispatch.php worker.php ./
COPY src ./src
ENTRYPOINT ["php", "/engine/dispatch.php"]
