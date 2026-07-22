<img src="docs/images/goblin-logo.png" alt="goblin" width="220">

# Goblin Operator

A Kubernetes operator with an LLM agent attached. When a pod breaks — OOMKilled,
Unschedulable, stalled rollout, quota exhausted — the operator wakes a scout
agent. The scout reads logs and events, works out what went wrong, and asks you
on Telegram before it changes anything. You approve, it patches the Deployment
and verifies the fix.

![goblin-scout](docs/images/goblin-scout.png)

## Prerequisites

- A Kubernetes cluster and `kubectl`
- Helm 3.8+
- An Anthropic API key — https://console.anthropic.com
- A Telegram bot: message [@BotFather](https://t.me/botfather), send `/newbot`,
  keep the token. Then send your new bot any message and read your chat ID:

  ```bash
  curl -s "https://api.telegram.org/bot<TOKEN>/getUpdates" | jq '.result[0].message.chat.id'
  ```

## Install

```bash
helm install goblin oci://registry-1.docker.io/sguldemond/goblin \
  --namespace goblin --create-namespace \
  --set llm.apiKey=<ANTHROPIC_API_KEY> \
  --set telegram.botToken=<BOT_TOKEN> \
  --set telegram.chatID=<CHAT_ID>
```

If you'd rather manage the credentials yourself, create Secrets and use
`llm.existingSecret` / `telegram.existingSecret` instead.

## Incident policies

An `IncidentPolicy` says what counts as an incident (a CEL expression over the
target object) and what the scout is allowed to do while that incident is open.
Nothing is detected until at least one exists. Sample policies for OOMKilled,
Unschedulable and stalled rollouts:

```bash
git clone https://github.com/sguldemond/goblin-operator
kubectl apply -k goblin-operator/operator/config/samples/policies -n goblin
```

Write your own by copying one of those files.

## Try it

Break something on purpose:

```bash
kubectl apply -f goblin-operator/scenarios/oom-killed.yaml
```

More in `scenarios/`: `unschedulable-nodeselector.yaml`,
`unschedulable-resources.yaml`, `stalled-rollout.yaml`, `quota-exceeded.yaml`.

Within a few seconds the scout messages you on Telegram with what it found and
what it wants to do. Reply to approve. Without Telegram
(`--set telegram.enabled=false`) the scout acts on its own — that is a
deliberate opt-out, not a default.
