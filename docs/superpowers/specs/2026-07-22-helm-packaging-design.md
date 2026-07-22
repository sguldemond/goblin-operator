# Helm packaging for goblin

Date: 2026-07-22

## Goal

Make goblin installable with a single `helm install`, requiring only an LLM API
key and Telegram credentials. Give users a pinned, stable-ish version instead of
a moving `latest`, while keeping a fast-moving `dev` channel for local work.

## Non-goals

- Multi-tenant or per-namespace installs. The chart installs one operator and
  one scout, as today.
- Publishing to Artifact Hub.
- Changing scout or operator behaviour beyond what the chart must configure.

## Chart shape

The chart lives at `dist/chart`, scaffolded with
`kubebuilder edit --plugins=helm/v1-alpha` and refreshed with `make helm`.

Generated, safe to regenerate:

- CRDs (`templates/crd/`)
- operator RBAC, scout ServiceAccount / Role / RoleBinding
- the scout-grant ValidatingAdmissionPolicy
- metrics service, network policies

Hand-maintained, excluded from regeneration and noted in the `Makefile`:

- `values.yaml`
- `templates/manager/manager.yaml` (the plugin already treats this as editable)
- `templates/secrets.yaml` (new)
- `templates/NOTES.txt`, `_helpers.tpl`

### values.yaml

```yaml
llm:
  apiKey: ""            # required unless existingSecret is set
  existingSecret: ""    # must contain key LLM_API_KEY
  provider: ""          # "" -> scout's built-in default (anthropic)
  model: ""             # "" -> provider default
telegram:
  enabled: true
  botToken: ""          # required when enabled, unless existingSecret
  chatID: ""            # required when enabled, unless existingSecret
  existingSecret: ""    # must contain TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID
operator:
  image:
    repository: sguldemond/goblin-operator
    tag: ""             # "" -> .Chart.AppVersion
    pullPolicy: IfNotPresent
scout:
  image:
    repository: sguldemond/goblin-scout
    tag: ""             # "" -> .Chart.AppVersion
    pullPolicy: ""      # "" -> Always for latest/dev, else IfNotPresent
```

### Required-value guard

`_helpers.tpl` calls `fail` with an actionable message when:

- neither `llm.apiKey` nor `llm.existingSecret` is set; or
- `telegram.enabled` is true and neither (`botToken` and `chatID`) nor
  `existingSecret` is set.

`telegram.enabled: false` is the deliberate opt-out. The scout then starts with
no notifier, which is existing behaviour (`agent/internal/scout/scout.go:94`),
and `NOTES.txt` says so plainly.

### Secrets

`templates/secrets.yaml` creates `goblin-scout-secrets` and, when Telegram is
enabled and no existing secret is named, `goblin-horn-secrets`. When
`existingSecret` is set the chart creates nothing and passes the name to the
operator.

### Install

```
helm install goblin oci://registry-1.docker.io/sguldemond/goblin \
  -n goblin-system --create-namespace \
  --set llm.apiKey=sk-... \
  --set telegram.botToken=... \
  --set telegram.chatID=...
```

## Operator changes

The scout Deployment is built in Go
(`operator/internal/controller/scout_deployment.go`), so secret names, image
and env are compiled in today. `ScoutReconciler` gains fields, set from new
flags in `operator/cmd/main.go`:

| flag | default | serves |
|---|---|---|
| `--scout-image` | `sguldemond/goblin-scout:latest` | already exists |
| `--scout-image-pull-policy` | `Always` | `scout.image.pullPolicy` |
| `--llm-secret` | `goblin-scout-secrets` | `llm.existingSecret` |
| `--horn-secret` | `goblin-horn-secrets` | `telegram.existingSecret` |
| `--llm-provider` | `""` | `llm.provider` |
| `--llm-model` | `""` | `llm.model` |

`desired()` uses these instead of the hardcoded constants and emits
`LLM_PROVIDER` / `LLM_MODEL` env vars only when non-empty, so the scout's own
defaults still apply when unset. `TELEGRAM_*` remain optional secret refs.

`desired()` is pure, so tests are table-driven in the existing controller
suite:

- custom secret names appear in the env `SecretKeyRef`s
- provider/model absent when empty, present when set
- pull policy honoured

## Release and distribution

Single workflow, `.github/workflows/build.yaml`, tags derived from the trigger
via `docker/metadata-action`:

| trigger | image tags |
|---|---|
| push to `master` | `dev` |
| push tag `v*` (e.g. `v20260722`) | `20260722` and `latest` |

`workflow_dispatch` allows a rebuild without a new git tag.

A third job runs on `v*` only:

1. `make helm`, then `helm lint dist/chart`
2. `helm package dist/chart --version "$CHART_VERSION" --app-version "${GITHUB_REF_NAME#v}"`
3. `helm push` to `oci://registry-1.docker.io/sguldemond`, reusing the existing
   Docker Hub credentials

`CHART_VERSION` is the `version:` field committed in `Chart.yaml`, bumped by
hand as semver. `appVersion` stays `dev` in git and is overridden at package
time, so a `git tag` is the only action needed to cut a release and the working
tree always installs the dev images.

OCI rather than GitHub Pages: no `index.yaml` to maintain and no second hosting
surface.

## Version semantics

A user installing chart `0.2.0` gets exactly `20260722`, because `tag: ""`
resolves to `.Chart.AppVersion`. Upgrading is `helm upgrade` to a newer chart
version. Users who want the moving tag set `tag: latest` explicitly; `dev` is
reserved for local development.

## Verification

- `make helm && helm lint dist/chart`
- `helm template` with only the three required values renders operator and
  scout with the pinned tag and correct secret refs
- `helm install --dry-run` fails with the guard message when a required value
  is missing, and when `telegram.enabled` is true with no credentials
- a real `helm install` into the kind cluster used by `scenarios/`, then run an
  existing scenario end to end
