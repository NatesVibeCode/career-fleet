# Getting free model access

**You probably do not need this.** Career Fleet's own workflow — discovering companies, ruling out
dealbreakers, and reading the survivors — uses explicit rules and the text of the postings
themselves. It does not call an AI model, so it needs no account, no key, and no credit.

You only need a model if you want the shared **engine** layer that ships in this repository: the
typed `career-screening` task, batch runs over many postings, or the offline demo.

If that is what you want, the engine runs entirely on models that cost **$0**. Pick one:

---

## Option 1 — OpenRouter (one signup, about two minutes)

1. Make a free account: <https://openrouter.ai/> → **Sign in**
2. Create a key: <https://openrouter.ai/keys> → **Create Key** → copy it (starts with `sk-or-`)
3. Give the key to the engine:

   ```bash
   export OPENROUTER_API_KEY="sk-or-paste-yours-here"
   python -m harness_fleet.cli doctor
   ```

**What you get:** the free models — names ending in `:free` (about twenty today) plus OpenRouter's
own `openrouter/free`, subject to OpenRouter's daily caps. See them with
`python -m harness_fleet.cli routes`.

---

## Option 2 — OpenCode (a terminal tool with its own free models)

```bash
# macOS / Linux
curl -fsSL https://opencode.ai/install | bash
# or:    brew install anomalyco/tap/opencode
# Windows: choco install opencode    (or use WSL)

opencode auth login          # choose "opencode" and sign in
```

Or run `opencode`, type `/connect`, pick `opencode`, and sign in at <https://opencode.ai/auth>.

**Worth knowing:** OpenCode's gateway asks for billing details during signup. Its free models stay
free; if you would rather not enter card details, use OpenRouter or a local model.

---

## Option 3 — A model on your own machine (no signup, no limits)

```bash
# install from https://ollama.com/download, then:
ollama pull llama3.2
python -m harness_fleet.cli routes add ollama/llama3.2 --provider ollama --free
```

---

## Checking what you have

```bash
python -m harness_fleet.cli doctor    # each AI tool installed? OpenRouter key set?
python -m harness_fleet.cli routes    # the $0 routes available right now
```
