# Vendored files

`xterm-5.5.0.js` and `xterm-5.5.0.css` are `lib/xterm.js` and `css/xterm.css` of the npm package `@xterm/xterm` 5.5.0 (MIT, see `LICENSE-xterm`), unchanged. The live screen draws the pane with it.

The package tarball was checked against the registry's integrity value:

```
https://registry.npmjs.org/@xterm/xterm/-/xterm-5.5.0.tgz
sha512-hqJHYaQb5OptNunnyAnkHyM8aCjZ1MEIDTQu1iIbbTD/xops91NB5yq1ZK/dC2JDbVWtF23zUtl9JE2NqwT87A==
```

To update: download the new tarball, check its `dist.integrity`, copy the two files under new names, and change the names in `app.js` and `miniapp.go`.
