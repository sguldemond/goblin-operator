# Goblin

Me goblin. Me live in cluster.

When pod die, me wake up. Me look at logs. Me look at events. Me figure out what went wrong.

Me tell you. You say yes. Me fix.

---

## What me do

- Pod OOMKilled? Me see it.
- Me read logs, check memory, look at everything.
- Me tell you what happened and what me think we should do.
- You approve. Me patch Deployment. Pod live again.

Me not touch anything without master say so.

---

## How me work

```
Pod die
  → Operator summon me
  → Me investigate
  → Me talk to you: kubectl attach -it <goblin-scout-pod>
  → You say yes → me fix
```

---

## How to start

```bash
# Give me power
cd operator && make install && make deploy

# Give me secret (API key)
kubectl create secret generic goblin-scout-secrets --from-env-file=agent/.env

# Break something
kubectl apply -f debug/oom-pod.yaml

# Talk to me when me appear
kubectl attach -it <goblin-scout-pod> -n default
```

---

## Where me live

```
operator/   me get summoned from here
agent/      me brain live here
debug/      oom-pod.yaml — for when you want to test me
```
