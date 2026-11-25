# TeraBox fixtures

Hand-authored from public descriptions of the TeraBox web client (shapes of
the share page, `/api/shorturlinfo`, `/share/list`, `/api/check/login` and
the membership query). They are NOT recordings of the live site: refresh
them (redacting `ndus`, `jsToken`, `dp-logid`, `bdstoken`, `browserid`,
`sign` and every `dlink`) whenever TeraBox changes its front-end, then update
`internal/hosters/terabox/endpoints.go`.

`{{BASE}}` is replaced by the test server URL at serve time.
