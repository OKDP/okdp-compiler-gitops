# Changelog

## 0.1.0 (2026-11-18)


### ⚠ BREAKING CHANGES

* the rule set okdp-compiler's mandatory one, rules/v1 gone, okdp-compiler v0.8.1, the okdp-objects chart 0.1.0 from a checkout
* the checks against the upstream charts okdp-gitops', fetched once per build, the compiler reading none, okdp-compiler v0.8.0 required
* the mandatory rules okdp-compiler's, the standard set added to them

### Features

* deploy each instance from a tree compiled by okdp-compiler ([896e449](https://github.com/OKDP/okdp-gitops/commit/896e449a8c24409b88cefe9309754c48c0bee6d1))
* each compile with the standard rule set, the tests that need it ([4cefe56](https://github.com/OKDP/okdp-gitops/commit/4cefe56bc6958221a95954524140d3adf2279cf6))
* okdp-compile, the CLI of the compiler and its targets ([a3d9f33](https://github.com/OKDP/okdp-gitops/commit/a3d9f33a181350944161c34fd00ba0e5f6f333e6))
* okdp-gitops, the compile of the sources and the workflows of a topology ([4d3192e](https://github.com/OKDP/okdp-gitops/commit/4d3192ef3a2d76ff101ffe11a47f5393fd37a7b3))
* the chart cache, the charts pulled from their registries ([bddc795](https://github.com/OKDP/okdp-gitops/commit/bddc7957cf44dde2a08eb927434c86f9111e2d7d))
* the deployments of a topology of repositories ([4574038](https://github.com/OKDP/okdp-gitops/commit/4574038731019d159bd980cbc2aff1995e8c5a9f))
* the examples, each compiled and compared with its output ([103ad34](https://github.com/OKDP/okdp-gitops/commit/103ad34b38e7f445af9c40f8dcb8404f7bdddc02))
* the Git writer of the deployments repository, the commits of the logged-in user ([4f87940](https://github.com/OKDP/okdp-gitops/commit/4f87940e0d717a5f87721092642951e2b62ab0a1))
* the Helm, Flux and Argo CD files of a release ([cba9bc2](https://github.com/OKDP/okdp-gitops/commit/cba9bc222ea8c03c2f492d3061d79a87f0373096))
* the packages of the git catalogs ([1abbc10](https://github.com/OKDP/okdp-gitops/commit/1abbc10aa5fa5d7e72d3a4446a11026ca3bf98ac))
* the reads and the edits of the tree, projects, instances and connections ([7448b4d](https://github.com/OKDP/okdp-gitops/commit/7448b4dd4d700d6802b3448458ab551eba0c0038))
* the rule set okdp-compiler's mandatory one, rules/v1 gone, okdp-compiler v0.8.1, the okdp-objects chart 0.1.0 from a checkout ([90bdd97](https://github.com/OKDP/okdp-gitops/commit/90bdd9783845b2dc4c89c678f87a648453b90762))
* the standard rule set, what a compile refuses and what it knows of a platform's names ([90cf6b2](https://github.com/OKDP/okdp-gitops/commit/90cf6b2214ba9d491828918236035074506b2694))


### Code Refactoring

* the checks against the upstream charts okdp-gitops', fetched once per build, the compiler reading none, okdp-compiler v0.8.0 required ([852cd79](https://github.com/OKDP/okdp-gitops/commit/852cd797174cd8572b7f644bf238c8c660abab1f))
* the mandatory rules okdp-compiler's, the standard set added to them ([de58502](https://github.com/OKDP/okdp-gitops/commit/de58502abc904bfd4503bb67620919438fe34228))
