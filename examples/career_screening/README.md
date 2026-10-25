# Career Screening Example

A ready-made recipe for screening employers, plus six sample job postings you can run right away.

**What it does:** reads a job posting or company blurb and answers five yes/no questions about it — does it name a real problem you can solve, does it use the tech you want, is there a concrete reason they're hiring, are the leaders technical, and does the work arrangement suit you. It then scores the employer out of 100 and quotes the exact sentences it relied on, so you can check the reasoning yourself instead of trusting a summary.

The sample postings are a deliberate mix: two are strong fits with a named problem and a real stack, one is an in-office dashboard job, and one is a thin AI wrapper chasing growth. Running it should rank them in that order.

---

## What's in This Folder

| File | What it is |
| --- | --- |
| `task.json` | The screening recipe: the five questions and how much each is worth |
| `sample_employers.csv` | Six example postings, with a company name and the posting text |

---

## Try It With the Samples

No accounts, no API keys, no cost — it uses a built-in fake model that returns predictable answers:

```bash
python -m harness_fleet.cli quickstart --demo --run-id demo-01
```

Open `runs/demo-01/clean_packet.csv` afterwards to see the scored, quote-backed rows. (This runs the shared engine that the prior Career Fleet was built on, which is why the command is longer than the usual ones below.)

---

## Screen Real Companies

Everyday Career Fleet commands, in the order you'd normally run them:

```bash
```


---

## Where to Go Next

- **Just want it to work?** Restart Claude Desktop (or Cursor) and ask it to screen employers for you — the bundled `career-fleet` skill walks the assistant through these same steps.
- **Tune the questions:** edit `profile.json` (what you want) and the five questions above; the score follows automatically.
- **Full walkthrough:** see `skills/career-fleet/SKILL.md`.
