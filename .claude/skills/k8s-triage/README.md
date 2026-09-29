# k8s-triage skill

A Claude skill that turns k8s-doctor findings into a verified root cause:
rank by real impact, correlate, confirm with read-only checks, and report.

```
k8s-triage/
├── SKILL.md                     workflow, hard safety rules, report format
├── references/
│   ├── correlation.md           how symptoms map to upstream causes
│   ├── findings.md              what each finding means + what evidence settles it
│   └── fleet-context.md         Karpenter / VPC CNI / Portworx / Artifactory / AL2023 patterns
├── scripts/
│   ├── summarize.py             k8s-doctor JSON -> tiered worklist + correlation hints (python3 stdlib)
│   └── confirm.sh               read-only evidence per hypothesis (write verbs refused)
└── settings.example.json        Claude Code permission allow/deny list
```

Requires k8s-doctor **v3.4.0+** (single-document `-o json`).

## Install (in the k8s-doctor repo)

```bash
mkdir -p .claude/skills
cp -r k8s-triage .claude/skills/
chmod +x .claude/skills/k8s-triage/scripts/*
# permissions: copy if you have no .claude/settings.json yet, otherwise merge the allow/deny lists
[ -f .claude/settings.json ] || cp .claude/skills/k8s-triage/settings.example.json .claude/settings.json
```

Claude Code discovers skills in `.claude/skills/` automatically. Ask for it
naturally: "triage bom-stage", "what's wrong in servicenow", or paste
k8s-doctor output.

## Use the scripts on their own (no LLM needed)

```bash
./k8s-doctor triage -n bom-stage -o json | python3 .claude/skills/k8s-triage/scripts/summarize.py
.claude/skills/k8s-triage/scripts/confirm.sh pullsecret bom-stage elasticsearch1-0
.claude/skills/k8s-triage/scripts/confirm.sh ns bom-stage
```

## Safety model (three layers)

1. SKILL.md forbids write commands and reading secret values.
2. `confirm.sh` refuses any kubectl verb except get/describe/logs/top/events/auth can-i.
3. `settings.example.json` denies write verbs at the Claude Code permission layer.

## Before pointing this at production clusters

Running it with Claude sends pod names, events, and log lines to Anthropic's
API. Get sign-off from IBM/Pfizer security first. If cloud LLMs are not
approved, the same files work with the local Ollama/Qwen setup: use SKILL.md
plus the three references as the system prompt, and let `summarize.py` and
`confirm.sh` do the deterministic work.

## Next

- Move the obvious ranking rules from `summarize.py` into k8s-doctor itself
  (for example, downgrade pull-secret warnings on Ready pods).
- Add a per-invocation `--context` flag to k8s-doctor that doesn't switch
  the shell's current context. Needed before any fleet-wide scheduled run.
- Expose triage/diagnose/node pressure as MCP tools, then point SKILL.md at them.
