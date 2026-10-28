# Changelog

## Unreleased

### Compatibility notes

* With WFP dispatch and its default DNS access enabled, split deployments now validate `NANOFLARE_DNS_CONFIG` on **nanoflared** at startup. Invalid configuration prevents startup; the runner's DNS configuration still applies to ordinary Workers. When using a file path, make that file available to both services. Set `NANOFLARE_WFP_DNS=false` (`--wfp-dns=false`) to reject namespace JSON DNS requests without public DoH fallback and skip the otherwise unused resolver configuration in split mode.

## [0.6.1](https://github.com/wen-templari/nanoflare/compare/v0.6.0...v0.6.1) (2026-10-26)


### Bug Fixes

* preserve HTTP origin identity through pinned proxy tunnels ([bd6a1a7](https://github.com/wen-templari/nanoflare/commit/bd6a1a7dbbe4c88cc996b9a0debbeaff60b93a1c))
* preserve pipelined named export getters across dispatch ([320c222](https://github.com/wen-templari/nanoflare/commit/320c2227b7eeac1ab74b8f15dce87be24e6bff6a))
* preserve pipelined named export getters across dispatch ([17902a6](https://github.com/wen-templari/nanoflare/commit/17902a635f2662f18379daf2cae9a3b4e6ef4ce4))
* preserve public HTTPS URLs in dynamic dispatcher ([47cc43e](https://github.com/wen-templari/nanoflare/commit/47cc43eaa2043e767be486fc2d781cf906d3bf4a))
* preserve public URL scheme in dynamic dispatcher ([5bc4994](https://github.com/wen-templari/nanoflare/commit/5bc4994d2ec826274e36c98237fc97b9c70aaf39))
* preserve Worker access identity headers ([e87c6c7](https://github.com/wen-templari/nanoflare/commit/e87c6c7f8f9e652476a14b7681d356a85031567f))
* preserve Worker access identity headers ([f382448](https://github.com/wen-templari/nanoflare/commit/f382448cb6385e480ab08f8107a249990c7b441e))
* prevent forged Worker identity headers ([ac897e8](https://github.com/wen-templari/nanoflare/commit/ac897e85b662293b68e5bcea78572604e08f20e1))
* scope dispatch namespace requests to the active organization ([f5862fa](https://github.com/wen-templari/nanoflare/commit/f5862fa7fbafeed1e70fb6ba4d5f477fb9ce2fcb))
* scope dispatch namespace requests to the active organization ([2ddcdef](https://github.com/wen-templari/nanoflare/commit/2ddcdef8d0a5d625dfc60deb690d4c099e2562b6))
* support trusted proxy DNS resolution for Worker egress ([fc0f791](https://github.com/wen-templari/nanoflare/commit/fc0f791d11c5101b85d55f85b2f545656dc3e6f4))
* support trusted proxy DNS resolution for Worker egress ([67d9669](https://github.com/wen-templari/nanoflare/commit/67d96692852689ada77fac22c121f113aa964570))

## [0.6.0](https://github.com/wen-templari/nanoflare/compare/v0.5.0...v0.6.0) (2026-10-23)


### Features

* add trusted Workers for Platforms support ([e195970](https://github.com/wen-templari/nanoflare/commit/e19597055cfa72c9dd6974b910efb353aa345cf4))
* isolate Worker revision publication across the platform ([02c65a8](https://github.com/wen-templari/nanoflare/commit/02c65a80624c78693a6599c78212c36f215cfcb5))
* publish worker revisions without restarting the shared runtime ([3de3642](https://github.com/wen-templari/nanoflare/commit/3de364261dedea19ea5a917d080ee873eb2d2344))
* support Cloudflare-compatible dispatcher uploads ([543fbaa](https://github.com/wen-templari/nanoflare/commit/543fbaae7cd3831cbd9fd23fbac43e80dbcf2482))
* support configuration-only worker variable updates ([55d304e](https://github.com/wen-templari/nanoflare/commit/55d304e880b1cddfb4c2432afb20c1b29a9dc401)), closes [#50](https://github.com/wen-templari/nanoflare/issues/50)
* support custom DNS for Node.js workers ([8c94576](https://github.com/wen-templari/nanoflare/commit/8c945768c8a17ef51b2d7990885c13aed3b17694))
* support updating worker hostnames ([6b3321a](https://github.com/wen-templari/nanoflare/commit/6b3321a48ca392d27ce204e0f1fd867f3a7fdc7e)), closes [#48](https://github.com/wen-templari/nanoflare/issues/48)
* unify Worker configuration with Cloudflare format ([7407b14](https://github.com/wen-templari/nanoflare/commit/7407b1435236e17ae2b388f2deedbda5a41b4b65))
* unify worker execution with shared workerd and Unix sockets ([3a4adde](https://github.com/wen-templari/nanoflare/commit/3a4adde363228667875d0eb002884998a8d9c8fd))
* verify WFP with official Cloudflare examples ([8495c74](https://github.com/wen-templari/nanoflare/commit/8495c74cf5a7f23b3193cd9fa046847725375b99))


### Bug Fixes

* cap worker compatibility dates ([6d6fafd](https://github.com/wen-templari/nanoflare/commit/6d6fafd697c788d7ef8d50314b5c08b704dfdae9))
* cap worker compatibility dates ([2fd0a3d](https://github.com/wen-templari/nanoflare/commit/2fd0a3d16ed5617ddff65a7a7c77c78a9796ce86))
* clarify invite authentication errors ([265bb36](https://github.com/wen-templari/nanoflare/commit/265bb36a9a0def38d4d0f41221f07fe0375c560c))
* include namespace permissions in UI token scope selector ([6c4d818](https://github.com/wen-templari/nanoflare/commit/6c4d818252b6e478db8e5d89b0f04616c721cf8f))
* include namespace permissions in UI token scope selector ([86468d7](https://github.com/wen-templari/nanoflare/commit/86468d7a49e9a8f51fdb7dbe0031406c1f744bbb))
* isolate invalid Worker revisions during runtime restoration ([3dba893](https://github.com/wen-templari/nanoflare/commit/3dba893cfe1009be4d9b8b8119bfa06b5700c978))
* isolate invalid Worker revisions during runtime restoration ([a104485](https://github.com/wen-templari/nanoflare/commit/a1044853c27072732edeadefa472f5d32cb144b9))
* move WFP execution consent to administrator configuration ([bdb36d3](https://github.com/wen-templari/nanoflare/commit/bdb36d378b6a56ecc42dbf32f81e7330c52ddbee))
* preserve public worker request URLs ([5baf7de](https://github.com/wen-templari/nanoflare/commit/5baf7de834dd12cbdecf6f3b213c8b88ab2b083d))
* proxy Cloudflare compatibility API through UI nginx ([fe723f1](https://github.com/wen-templari/nanoflare/commit/fe723f1f84c8d77ad63cf223534f9910f42c80f9))
* proxy Cloudflare compatibility API through UI nginx ([d585e19](https://github.com/wen-templari/nanoflare/commit/d585e1902239bb522441b6de0d790bdee4cb3340))
* reserve runtime ports during startup ([2b848fe](https://github.com/wen-templari/nanoflare/commit/2b848fedcc3d0ea32b78542b0290902ec71b9f32))
* resolve DNS promises shim dependency ([b712927](https://github.com/wen-templari/nanoflare/commit/b712927fd020e03b47223f5e238ccc0e6ca04cd2))
* resolve DNS shims from nested worker modules ([28aa202](https://github.com/wen-templari/nanoflare/commit/28aa2025fab39240f2b5939bf748cde568061376))
* resolve DNS shims from nested worker modules ([26d6ab4](https://github.com/wen-templari/nanoflare/commit/26d6ab47247059ed376625c0a149ff3faad2303f))
* reuse runtime ports after allocator wraps ([feea252](https://github.com/wen-templari/nanoflare/commit/feea2523b780de431e67ee4cbc0f79ca055a82bd))
* reuse runtime ports after allocator wraps ([96deaaf](https://github.com/wen-templari/nanoflare/commit/96deaafaede4db7d40369d2367f720247cf48c27))
* scope dynamic resource authority to revision grants ([c6d7af3](https://github.com/wen-templari/nanoflare/commit/c6d7af306f783ad321b5d308632c98535a992479))
* sign in existing users from invite links ([75d0fc0](https://github.com/wen-templari/nanoflare/commit/75d0fc001303a679a6410d2fe095aca9df909ec1))
* sign in existing users from invite links ([ba2c57d](https://github.com/wen-templari/nanoflare/commit/ba2c57d936d8334778f5dc969543f8e2f3c5dd6d))
* tolerate null deployment storage bindings during deletion ([a7de4be](https://github.com/wen-templari/nanoflare/commit/a7de4be68053e49bb442d5936012b63f3210cb62))
* tolerate null deployment storage bindings during deletion ([4b1c935](https://github.com/wen-templari/nanoflare/commit/4b1c935dcfafb49e484c0dabae9e2afb03a53bd9))
* use SSO for organization invites ([1caa3b2](https://github.com/wen-templari/nanoflare/commit/1caa3b28c556bca570b0d3e2715145ff33221db8))
* validate compatibility publications without aborting active generations ([34eb3ac](https://github.com/wen-templari/nanoflare/commit/34eb3ac7b41a7f63f52ef92143bbf4e224f3f476))

## [0.5.0](https://github.com/wen-templari/nanoflare/compare/v0.4.0...v0.5.0) (2026-08-29)


### Features

* add gateway tracing and reduce request latency ([f5481a1](https://github.com/wen-templari/nanoflare/commit/f5481a11cfadbea6b4181c9e538088e5884a8d64))
* add MCP worker templates ([c25d62d](https://github.com/wen-templari/nanoflare/commit/c25d62df02d397c7ebcff11951ce98d5fcb74e11))
* add OAuth MCP calculator example ([051af17](https://github.com/wen-templari/nanoflare/commit/051af1792a58788bc57dfa367d05f86950c9d535))
* add OAuth provider KV and MCP templates ([607374f](https://github.com/wen-templari/nanoflare/commit/607374f4f73f1e6d4f2fc0ba1f46e92c301e27b6))
* support OAuth provider KV operations ([8eaa9d4](https://github.com/wen-templari/nanoflare/commit/8eaa9d471c0d4aacefc050cf3759bd9839c6df6a))


### Bug Fixes

* bound duration telemetry storage ([70da06e](https://github.com/wen-templari/nanoflare/commit/70da06eca6d75da808fc23ec1c9b099c042e5e81))
* cache worker runtime deployments ([3c84335](https://github.com/wen-templari/nanoflare/commit/3c84335f13ad8a68f57d1ec06c9f71be50013d3d))
* preserve worker redirects in gateway ([761a407](https://github.com/wen-templari/nanoflare/commit/761a40706f216df5ef5ca467116d6a963c0105a9))
* prevent repeated worker output identity markers ([5e234d3](https://github.com/wen-templari/nanoflare/commit/5e234d3323c32f08a6a99aac26e246c20b456370))
* prevent repeated worker output identity markers ([bcc9dd0](https://github.com/wen-templari/nanoflare/commit/bcc9dd0c6d2f66cd64d13fc4b87d8785773fd05b))
* raise worker deployment size limit ([3d76f0d](https://github.com/wen-templari/nanoflare/commit/3d76f0dcb79149ae9bafa0a695f3791cc977f1b4))
* scope worker log identity to async context ([cbe1af7](https://github.com/wen-templari/nanoflare/commit/cbe1af7060da77a853908c6d3140a2a7a7e9e965))
* scope worker log identity to async context ([4ced291](https://github.com/wen-templari/nanoflare/commit/4ced291db24180d353fa6b6693cf51ccfd9edcc5))

## [0.4.0](https://github.com/wen-templari/nanoflare/compare/v0.3.1...v0.4.0) (2026-08-09)


### Features

* generate typed service bindings ([43ba82e](https://github.com/wen-templari/nanoflare/commit/43ba82eeb918f63c263f349a0d7337b69125841a))
* update cli ([3bcbb7d](https://github.com/wen-templari/nanoflare/commit/3bcbb7d7c8a3d0ddf23ceec59e801e7ba46a1ad3))


### Bug Fixes

* add Vite plugin release recovery ([311d9da](https://github.com/wen-templari/nanoflare/commit/311d9da27c616a5d4af6ae527744d29665ac9c7c))

## [0.3.1](https://github.com/wen-templari/nanoflare/compare/v0.3.0...v0.3.1) (2026-08-08)


### Bug Fixes

* approve workerd build script ([a8c450c](https://github.com/wen-templari/nanoflare/commit/a8c450c8c337a471c741f75b1ec6e2bf8a145393))
* fix ci test ([238c49c](https://github.com/wen-templari/nanoflare/commit/238c49c46d286065d76a828af31786c0b99fa9e0))

## [0.3.0](https://github.com/wen-templari/nanoflare/compare/v0.2.3...v0.3.0) (2026-08-08)


### Features

* add Nanoflare starter templates ([59a81a7](https://github.com/wen-templari/nanoflare/commit/59a81a7b7706d4d69edb075b87d06bb816146a77))
* **vite-plugin:** add build support ([0b9cf72](https://github.com/wen-templari/nanoflare/commit/0b9cf72209a8a898759b2b7df4041665da4a466a))


### Bug Fixes

* derive compatibility fallback date at startup ([621b1bf](https://github.com/wen-templari/nanoflare/commit/621b1bf514c5daa8b9b73ffa9f9061e7da8e7258))
* keep create templates on current CLI ([212e729](https://github.com/wen-templari/nanoflare/commit/212e729cc1b81984008033f32ced4ea0cf06dccd))

## [0.2.3](https://github.com/wen-templari/nanoflare/compare/v0.2.2...v0.2.3) (2026-08-08)


### Bug Fixes

* simplify core release workflow ([82cecdc](https://github.com/wen-templari/nanoflare/commit/82cecdc350b828f6187914050194f1e95b44255a))

## [0.2.2](https://github.com/wen-templari/nanoflare/compare/v0.2.1...v0.2.2) (2026-08-08)


### Bug Fixes

* publish npm packages on core releases ([4e27ece](https://github.com/wen-templari/nanoflare/commit/4e27ece02d529465f5338ea20cc2d3c61c590b2e))

## [0.2.1](https://github.com/wen-templari/nanoflare/compare/v0.2.0...v0.2.1) (2026-08-08)


### Bug Fixes

* keep CLI platform dependencies lockfile-stable ([93566f4](https://github.com/wen-templari/nanoflare/commit/93566f41eadcec1c7764b59f430aea07e9cbc1b1))
* keep CLI platform dependencies lockfile-stable ([52be3f8](https://github.com/wen-templari/nanoflare/commit/52be3f8d4a65679a711b49ff70636c115a3e42d5))

## [0.2.0](https://github.com/wen-templari/nanoflare/compare/v0.1.6...v0.2.0) (2026-08-08)

### Features

- **gallery:** add local Vite worker development ([52222e7](https://github.com/wen-templari/nanoflare/commit/52222e7fad94c28b5fc579f196442f0037c8bde0))
- **gallery:** add local Vite worker development ([240c486](https://github.com/wen-templari/nanoflare/commit/240c4867e53ff6c613d78d4981da10d7814731c7))

### Bug Fixes

- add package repository metadata ([4578122](https://github.com/wen-templari/nanoflare/commit/45781222f5551b3d91674247e989520abd8a0166))

## [0.1.6](https://github.com/wen-templari/nanoflare/compare/v0.1.5...v0.1.6) (2026-08-08)

### Bug Fixes

- **vite-plugin:** set publish config to public access ([e0ffb81](https://github.com/wen-templari/nanoflare/commit/e0ffb81f40da01d5f081e8c5e38a218c51c66ec9))
