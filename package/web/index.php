<?php
// The configure page is served by the registry itself; send the browser there.
// (The registry redirects on to https:// once a certificate is installed.)
$port = 5000;
$file = __DIR__ . '/port';
if (is_readable($file) && ctype_digit(trim(file_get_contents($file)))) {
    $port = (int) trim(file_get_contents($file));
}
$host = explode(':', $_SERVER['HTTP_HOST'])[0];
header("Location: http://{$host}:{$port}/");
?>
