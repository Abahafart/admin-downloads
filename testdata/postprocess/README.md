# Fixtures de postprocess

Archivos 7z y RAR diminutos para los tests de `internal/postprocess`. En el
entorno de desarrollo no hay ningún compresor 7z/rar, así que **están
escritos a mano** por un generador reproducible que sigue las
especificaciones de los formatos (7zFormat.txt de 7-Zip; notas técnicas de
RAR 5.0 y RAR 4.x de rarlab.com):

```sh
go test -tags genfixtures -run TestGenerateFixtures ./internal/postprocess
```

(`internal/postprocess/gen_fixtures_test.go`). Todas las entradas son fijas
(sal, IV, fechas), así que regenerarlos produce los mismos bytes. Solo usan
el método "copy"/"store" (la descompresión es cosa de las librerías) más el
cifrado 7zAES y AES-256 de RAR5. Los RAR se validaron con
`unrar t` (UNRAR 7.20): todos dan "All OK" y `enc.rar` responde
"Incorrect password" con una contraseña errónea. Los zip no están aquí: los
tests los generan con `archive/zip` (incluido el cifrado ZipCrypto y
WinZip AES).

| Archivo | Contenido | Contraseña |
|---|---|---|
| `copy.7z` | `hello.txt`, `dir/nested.txt` (coder Copy) | — |
| `aes.7z` | `secret.txt` (un único coder 7zAES, 2^6 rondas, sin sal) | `secreto7z` |
| `traversal.7z` | `../evil.txt` (debe rechazarse), `ok.txt` | — |
| `split.7z.001`, `split.7z.002` | `split.txt` (640 bytes), un 7z cortado en dos volúmenes | — |
| `stored.rar` | RAR5: directorio `dir`, `hello.txt`, `dir/nested.txt` | — |
| `enc.rar` | RAR5: `secret.txt` cifrado (AES-256, KDF 2^15, con valor de comprobación) | `secretorar` |
| `traversal.rar` | RAR5: `../evil.txt` (debe rechazarse), `ok.txt` | — |
| `multi.part1.rar`, `multi.part2.rar` | RAR5 multivolumen (nombres nuevos): `big.txt` repartido entre los dos, `small.txt` en el segundo | — |
| `old.rar`, `old.r00` | RAR4 multivolumen (nombres antiguos): `old.txt` repartido entre los dos | — |
