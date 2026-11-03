<?php $host = explode(':', $_SERVER['HTTP_HOST'])[0]; ?>
<!doctype html>
<meta charset="utf-8">
<title>OCI Registry</title>
<h1>OCI Registry</h1>
<p>Serving on <code><?php echo htmlspecialchars($host); ?>:5000</code> (plain HTTP).</p>
<p>Pull: <code>--from <?php echo htmlspecialchars($host); ?>:5000/NAME:TAG</code></p>
