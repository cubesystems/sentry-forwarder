<?php

require_once '../vendor/autoload.php';

use GuzzleHttp\Client;
use Psr\Http\Message\ResponseInterface as Response;
use Psr\Http\Message\ServerRequestInterface as Request;
use Slim\Factory\AppFactory;
use Symfony\Component\Yaml\Yaml;

$app = AppFactory::create();
$app->addRoutingMiddleware();
$errorMiddleware = $app->addErrorMiddleware(false, false, false);

// Configuration
$mappings = $data = Yaml::parseFile('../config.yaml')['dsn_mapping'];

function getOldKey($headerValue)
{
    $headerParts = explode(',', $headerValue);
    $values = [];

    foreach ($headerParts as $part) {
        list($key, $value) = explode('=', trim($part), 2);
        $values[trim($key)] = trim($value, '"');
    }

    return $values['sentry_key'];
}

function getMapping($oldKey, $mappings)
{
    foreach ($mappings as $mapping) {
        $oldURI = parse_url($mapping['old']);

        if ($oldURI['user'] === $oldKey) {
            return [
                'old_uri' => $oldURI,
                'new_uri' => parse_url($mapping['new']),
                'old_dsn' => $mapping['old'],
                'new_dsn' => $mapping['new'],
            ];
        }
    }

    return null;
}

function convertPayload($payload, $mapping)
{
    $payload = (gzdecode($payload));

    $escapedOldDSN = str_replace('/', '\/', $mapping['old_dsn']);
    $escapedNewDSN = str_replace('/', '\/', $mapping['new_dsn']);

    $payload = str_replace($escapedOldDSN, $escapedNewDSN, $payload);
    $payload = str_replace($mapping['old_uri']['user'], $mapping['new_uri']['user'], $payload);

    return gzencode($payload);
}

$app->post('/{path:.*}', function (Request $request, Response $response) use ($mappings) {
    $client = new Client();

    $oldKey = getOldKey($request->getHeaderLine('X-Sentry-Auth'));
    $mapping = getMapping($oldKey, $mappings);

    if (is_null($mapping)) {
        error_log("Unknown old sentry DSN key: " . $oldKey);

        $response->getBody()->write(json_encode(['error' => 'unknown DSN for forwarding']));
        return $response->withStatus(500)->withHeader('Content-Type', 'application/json');
    }

    $headers = [];

    foreach ($request->getHeaders() as $key => $value) {
        $headers[$key] = $value[0];
    }

    $headers['X-Sentry-Auth'] = str_replace($mapping['old_uri']['user'], $mapping['new_uri']['user'], $headers['X-Sentry-Auth']);
    $headers['Host'] = $mapping['new_uri']['host'];

    $newUrl = $mapping['new_uri']['scheme'] . '://' . $mapping['new_uri']['host'] . '/api' . $mapping['new_uri']['path'] . '/envelope/';

    // Log the full request URI and the new URL (optional)
    error_log("Forwarding from " . $mapping['old_dsn'] . " to " . $mapping['new_dsn']);

    // Get the JSON body from the incoming request
    $data = $request->getBody()->getContents();

    // Forward the request to the new Sentry DSN
    try {
        $res = $client->request('POST', $newUrl, [
            'body' => convertPayload($data, $mapping),
            'headers' => $headers,
        ]);

        // Respond with the status and body from the new Sentry DSN
        $response->getBody()->write($res->getBody()->getContents());
        return $response->withStatus($res->getStatusCode())->withHeader('Content-Type', 'application/json');
    } catch (Exception $e) {
        // Handle exceptions
        $response->getBody()->write(json_encode(['error' => $e->getMessage()]));
        return $response->withStatus(500)->withHeader('Content-Type', 'application/json');
    }
});

$app->run();
