FROM php:8.3-fpm-alpine

WORKDIR /usr/share/www

COPY --from=composer:2 /usr/bin/composer /usr/bin/composer


COPY . .

RUN composer install


CMD ["php","-S", "0.0.0.0:8000", "-t", "public/"]