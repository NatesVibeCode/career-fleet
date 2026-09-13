# Authorized Ideal Employer Profile (IEP) Inference & Calibration

When an operator, engineer, or builder wants to discover and evaluate target companies, **never interrogate them with generic questionnaires**, and **never jump blindly into job boards with vague titles** like "software engineer" or "business development".

The agent harness may inspect only context the user explicitly provides or authorizes—past project artifacts, resumes, repositories, or external profiles—to infer the candidate's core architectural wedge, preferred company topology, and non-negotiables. It must not scan unrelated files or accounts by assumption.

Only after this authorized scan should the agent conduct a focused, 3-question interview for anything missing.

---

## The 3-Question Targeted Interview Protocol

If any of the 6 core signals are missing after the autonomous scan, present these 3 focused questions:

### Question 1: Non-Negotiable Dealbreakers
> *"What hard constraints immediately disqualify an opportunity for you? (e.g., in-person office mandates outside your metro, company size exceeding 80 people, specific tech stacks, or pure quota grinding)?"*

### Question 2: The Hiring Catalyst & Problem Wedge
> *"What acute operational breaking point or technical challenge are you most energized to solve for a founder? (e.g., taking an unsiloed commercial engine from 0 to 1, rewriting a legacy v1 data pipeline, or building regulated compliance infrastructure)?"*

### Question 3: 2–3 Anchor Exemplars
> *"Name 2 or 3 companies (past or present) whose engineering rigor, product defensibility, and operational culture represent your target benchmark."*

---

## Profile Synthesis

Initialize `profile.json` once with `career-fleet profile --init`, confirm the inferred values with the user, then edit the generated JSON with the answers. The command refuses to overwrite an existing profile unless `--force` is supplied.
