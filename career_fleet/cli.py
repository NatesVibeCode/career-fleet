"""Unified CLI for career-fleet: Autonomous career & employer intelligence engine."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from career_fleet import __version__
from career_fleet.profile import IdealEmployerProfile
from career_fleet.setup import install_skill
from career_fleet.store import CareerStore
from career_fleet.lanes import (
    run_lane1_sourcing,
    run_lane2_triage,
    run_lane3_systems,
    run_lane4_culture,
)


from typing import Optional

# Reconfigure stdout/stderr on platforms (like Windows cp1252) where console encoding fails on unicode
if sys.stdout and hasattr(sys.stdout, "reconfigure"):
    try:
        sys.stdout.reconfigure(errors="replace")
    except Exception:
        pass
if sys.stderr and hasattr(sys.stderr, "reconfigure"):
    try:
        sys.stderr.reconfigure(errors="replace")
    except Exception:
        pass


def _ok(msg: str) -> str:
    try:
        "\u2713".encode(sys.stdout.encoding or "utf-8")
        return f"✓ {msg}"
    except Exception:
        return f"[OK] {msg}"


def _bullet() -> str:
    try:
        "\u2022".encode(sys.stdout.encoding or "utf-8")
        return "•"
    except Exception:
        return "*"


def _positive_int(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be an integer") from exc
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be at least 1")
    return parsed


def get_profile(path: Optional[str] = None) -> IdealEmployerProfile:
    p = Path(path) if path is not None else Path("profile.json")
    if p.exists():
        return IdealEmployerProfile.load(p)
    if path is not None:
        raise FileNotFoundError(f"Profile file not found at: {p}")
    return IdealEmployerProfile()


def cmd_init(args):
    db_path = getattr(args, "db", "career_fleet.db")
    store = CareerStore(db_path)
    profile_path = Path("profile.json")
    if not profile_path.exists():
        prof = IdealEmployerProfile()
        prof.save(profile_path)
        print(_ok(f"Initialized default Ideal Employer Profile at {profile_path}"))
    print(_ok(f"Initialized CareerStore database at {db_path}"))


def cmd_profile(args):
    prof_path = getattr(args, "path", "profile.json")
    profile_exists = Path(prof_path).exists()
    if getattr(args, "init", False) and profile_exists and not getattr(args, "force", False):
        print(f"Error: Profile already exists at {prof_path}; use --force to replace it.", file=sys.stderr)
        return 1
    if getattr(args, "init", False) or not profile_exists:
        prof = IdealEmployerProfile()
        prof.save(prof_path)
        print(_ok(f"Wrote initial Ideal Employer Profile to {prof_path}"))
        return 0

    try:
        prof = IdealEmployerProfile.load(prof_path)
    except (OSError, ValueError) as exc:
        print(f"Error: Could not load profile {prof_path}: {exc}", file=sys.stderr)
        return 1
    print("==========================================================================================")
    print(f"                      IDEAL EMPLOYER PROFILE: {prof.profile_name} (v{prof.version})")
    print("==========================================================================================")
    print(f"Capabilities:")
    for c in prof.wedge_capabilities:
        print(f"  {_bullet()} {c}")
    print(f"\nRequired Stack:")
    for s in prof.required_stack:
        print(f"  {_bullet()} {s}")
    print(f"\nHard Dealbreakers:")
    print(f"  {_bullet()} Max Headcount: {prof.dealbreakers.max_headcount}")
    print(f"  {_bullet()} Policy:        {prof.dealbreakers.policy}")
    print(f"  {_bullet()} Disallowed:    {', '.join(prof.dealbreakers.disallowed_locations)}")
    print(f"  {_bullet()} Reject Wrapper:{prof.dealbreakers.reject_thin_wrappers}")
    print(f"  {_bullet()} Reject Quota:  {prof.dealbreakers.reject_pure_quota}")
    print(f"  {_bullet()} Candidate TZ:  {prof.dealbreakers.candidate_timezone or '-'}")
    print(f"  {_bullet()} TZ Overlap:    {prof.dealbreakers.min_timezone_overlap_hours}h")
    print(f"\nAnchor Exemplars:  {', '.join(prof.anchor_companies)}")
    print("==========================================================================================")
    return 0


def cmd_discover(args):
    store = CareerStore(getattr(args, "db", "career_fleet.db"))
    try:
        res = run_lane1_sourcing(
            store=store,
            source_type=args.source,
            target=args.target,
            max_items=getattr(args, "max", 50),
        )
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"Error: Lane 1 discovery failed: {exc}", file=sys.stderr)
        return 1
    if res.get("status") != "success":
        print(f"Error: Lane 1 discovery failed: {res.get('error', 'unknown error')}", file=sys.stderr)
        return 1
    print(_ok(f"Lane 1 Discovery completed: {res['companies_discovered']} companies discovered, {res['postings_added']} postings ingested."))
    if res.get("pages_skipped"):
        print(f"Skipped pages: {res['pages_skipped']}")
    return 0


def cmd_triage(args):
    store = CareerStore(getattr(args, "db", "career_fleet.db"))
    try:
        prof = get_profile(getattr(args, "profile", None))
    except (OSError, ValueError) as exc:
        print(f"Error: Could not load profile: {exc}", file=sys.stderr)
        return 1
    res = run_lane2_triage(store, prof)
    print("==========================================================================================")
    print("                              LANE 2: GATEKEEPER TRIAGE                                    ")
    print("==========================================================================================")
    print(f"Total Companies Triaged: {res['total_triaged']}")
    print(f"Surviving Candidates:   {res['survivors']} (proceed to Lane 3)")
    print(f"Disqualified Dealbreakers: {res['disqualified']} (dropped)")
    print("==========================================================================================")
    return 0


def cmd_recon(args):
    store = CareerStore(getattr(args, "db", "career_fleet.db"))
    try:
        prof = get_profile(getattr(args, "profile", None))
    except (OSError, ValueError) as exc:
        print(f"Error: Could not load profile: {exc}", file=sys.stderr)
        return 1
    lane = getattr(args, "lane", "all")

    if lane in ("systems", "all"):
        res3 = run_lane3_systems(store, prof)
        print(_ok(f"Lane 3 Systems Wedge evaluation completed on {res3['evaluated']} companies."))

    if lane in ("culture", "all"):
        res4 = run_lane4_culture(store, prof)
        print(_ok(f"Lane 4 Culture Recon evaluation completed on {res4['evaluated']} companies."))
    return 0


def cmd_list(args):
    store = CareerStore(getattr(args, "db", "career_fleet.db"))
    status_filter = getattr(args, "status", None)
    companies = store.list_companies(status=status_filter)

    print("==========================================================================================")
    print(f"                            TRACKED COMPANIES [{status_filter or 'ALL'}]")
    print("==========================================================================================")
    print(f"{'ID':<20} {'Name':<24} {'Status':<14} {'ATS / Domain':<26}")
    print("-" * 90)
    for c in companies:
        ats = f"{c.get('ats_provider') or '-'}:{c.get('ats_token') or '-'}" if c.get("ats_provider") else (c.get("domain") or "-")
        print(f"{c['id']:<20} {c['name'][:22]:<24} {c['status']:<14} {ats[:25]:<26}")
    print("==========================================================================================")
    print(f"Total: {len(companies)} companies.\n")
    return 0


def cmd_dossier(args):
    store = CareerStore(getattr(args, "db", "career_fleet.db"))
    dossier = store.get_company_dossier(args.company)
    if not dossier:
        print(f"Error: Company '{args.company}' not found.")
        return 1

    print("==========================================================================================")
    print(f"                         COMPANY DOSSIER: {dossier['name']} ({dossier['id']})")
    print("==========================================================================================")
    print(f"Domain:      {dossier.get('domain') or '-'}")
    print(f"Status:      {dossier.get('status')}")
    if dossier.get("disqualification_reason"):
        print(f"Reason:      {dossier['disqualification_reason']}")
    print(f"Postings:    {len(dossier.get('jobs', []))} active postings captured")
    print("\nEvaluations:")
    for ev in dossier.get("evaluations", []):
        quotes = json.loads(ev.get("quotes_json", "[]"))
        print(f"  [{ev['lane']}] Verdict: {ev['verdict']} (Score: {ev['score']}) via {ev.get('model_used') or 'heuristic'}")
        print(f"    Rationale: {ev['rationale']}")
        if quotes:
            print("    Quotes:")
            for q in quotes:
                print(f"      {_bullet()} \"{q}\"")
    print("==========================================================================================")
    return 0


def cmd_export(args):
    store = CareerStore(getattr(args, "db", "career_fleet.db"))
    qualified = [c for c in store.list_companies() if c["status"] == "qualified"]
    dossiers = [store.get_company_dossier(c["id"]) for c in qualified]
    out_path = Path(getattr(args, "output", "qualified_targets.json"))
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(dossiers, indent=2), encoding="utf-8")
    print(_ok(f"Exported {len(dossiers)} qualified company dossiers to {out_path}"))
    return 0


def cmd_setup(args):
    try:
        report = install_skill(args.workspace_root, force=args.force)
    except (OSError, RuntimeError, ValueError) as exc:
        print(f"Error: Could not install Career Fleet skill: {exc}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(report))
    else:
        print(_ok(f"Career Fleet skill {report['action']} at {report['skill_path']}"))
    return 0


def main():
    parser = argparse.ArgumentParser(
        prog="career-fleet",
        description="Autonomous Career & Employer Intelligence Engine with Multi-Lane Screening.",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    p_init = subparsers.add_parser("init", help="Initialize SQLite DB and profile template")
    p_init.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_init.set_defaults(func=cmd_init)

    p_prof = subparsers.add_parser("profile", help="Inspect or generate Ideal Employer Profile")
    p_prof.add_argument("--path", default="profile.json", help="Path to profile.json")
    p_prof.add_argument("--init", action="store_true", help="Generate fresh default profile")
    p_prof.add_argument("--force", action="store_true", help="Replace an existing profile when used with --init")
    p_prof.set_defaults(func=cmd_profile)

    p_disc = subparsers.add_parser("discover", help="Lane 1: Sourcing & discovery")
    p_disc.add_argument("--source", required=True, choices=["yc", "greenhouse", "ashby", "lever", "site"], help="Source type")
    p_disc.add_argument("--target", required=True, help="Batch, tag, board token, or origin URL")
    p_disc.add_argument("--max", type=_positive_int, default=50, help="Maximum items to ingest")
    p_disc.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_disc.set_defaults(func=cmd_discover)

    p_trig = subparsers.add_parser("triage", help="Lane 2: Gatekeeper triage (dealbreakers)")
    p_trig.add_argument("--profile", default=None, help="Path to profile.json (default: ./profile.json when present)")
    p_trig.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_trig.set_defaults(func=cmd_triage)

    p_rec = subparsers.add_parser("recon", help="Lanes 3 & 4: Systems wedge & culture recon")
    p_rec.add_argument("--lane", choices=["systems", "culture", "all"], default="all", help="Which lane to run")
    p_rec.add_argument("--profile", default=None, help="Path to profile.json (default: ./profile.json when present)")
    p_rec.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_rec.set_defaults(func=cmd_recon)

    p_list = subparsers.add_parser("list", help="List tracked companies")
    p_list.add_argument("--status", choices=["discovered", "triaged", "qualified", "disqualified"], help="Filter by status")
    p_list.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_list.set_defaults(func=cmd_list)

    p_dos = subparsers.add_parser("dossier", help="Inspect company dossier")
    p_dos.add_argument("--company", required=True, help="Company ID")
    p_dos.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_dos.set_defaults(func=cmd_dossier)

    p_exp = subparsers.add_parser("export", help="Export qualified company dossiers")
    p_exp.add_argument("--output", default="qualified_targets.json", help="Output JSON path")
    p_exp.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_exp.set_defaults(func=cmd_export)

    p_setup = subparsers.add_parser("setup", help="Install the bundled assistant skill into a workspace")
    p_setup.add_argument("--workspace-root", default=".", help="Workspace directory (default: current directory)")
    p_setup.add_argument("--force", action="store_true", help="Replace a different existing Career Fleet skill")
    p_setup.add_argument("--json", action="store_true", help="Emit machine-readable JSON")
    p_setup.set_defaults(func=cmd_setup)

    args = parser.parse_args()
    result = args.func(args)
    return result if isinstance(result, int) else 0


if __name__ == "__main__":
    raise SystemExit(main())
