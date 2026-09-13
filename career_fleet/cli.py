"""Unified CLI for career-fleet: deterministic career and employer screening."""
from __future__ import annotations

import argparse
import json
import sqlite3
import sys
import textwrap
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


def _workspace_path(value: str | Path, workspace_root: str | Path = ".") -> Path:
    candidate = Path(value).expanduser()
    if candidate.is_absolute():
        return candidate
    return Path(workspace_root).expanduser().resolve() / candidate


def get_profile(
    path: Optional[str] = None,
    store: CareerStore | None = None,
    workspace_root: str | Path = ".",
) -> IdealEmployerProfile:
    p = _workspace_path(path, workspace_root) if path is not None else _workspace_path("profile.json", workspace_root)
    if p.exists():
        profile = IdealEmployerProfile.load(p)
        if store is not None:
            store.save_profile(profile)
        return profile
    if path is None and store is not None:
        stored_profile = store.load_profile()
        if stored_profile is not None:
            return stored_profile
    raise FileNotFoundError(
        f"No profile found at {p} or in the selected database. Run 'career-fleet profile --init' or pass --profile PATH."
    )


def _open_store(db_path: str, workspace_root: str | Path = ".") -> CareerStore | None:
    resolved_db = _workspace_path(db_path, workspace_root)
    try:
        return CareerStore(resolved_db)
    except (OSError, sqlite3.Error) as exc:
        print(f"Error: Could not open database {resolved_db}: {exc}", file=sys.stderr)
        return None


def cmd_init(args):
    workspace = Path(getattr(args, "workspace_root", ".")).expanduser().resolve()
    db_path = _workspace_path(getattr(args, "db", "career_fleet.db"), workspace)
    store = _open_store(str(db_path), workspace)
    if store is None:
        return 1
    profile_path = workspace / "profile.json"
    try:
        if profile_path.exists():
            prof = IdealEmployerProfile.load(profile_path)
        elif not getattr(args, "init", False) and (stored_profile := store.load_profile()) is not None:
            prof = stored_profile
            prof.save(profile_path)
        else:
            prof = IdealEmployerProfile()
            prof.save(profile_path)
            print(_ok(f"Initialized default Ideal Employer Profile at {profile_path}"))
        store.save_profile(prof)
    except (OSError, ValueError, sqlite3.Error) as exc:
        print(f"Error: Could not initialize profile {profile_path}: {exc}", file=sys.stderr)
        return 1
    print(_ok(f"Initialized CareerStore database at {db_path}"))


def cmd_profile(args):
    workspace = Path(getattr(args, "workspace_root", ".")).expanduser().resolve()
    prof_path = _workspace_path(getattr(args, "path", "profile.json"), workspace)
    profile_exists = Path(prof_path).exists()
    if getattr(args, "init", False) and profile_exists and not getattr(args, "force", False):
        print(f"Error: Profile already exists at {prof_path}; use --force to replace it.", file=sys.stderr)
        return 1

    store = _open_store(getattr(args, "db", "career_fleet.db"), workspace)
    if store is None:
        return 1
    try:
        if getattr(args, "init", False) or (not profile_exists and store.load_profile() is None):
            prof = IdealEmployerProfile()
            prof.save(prof_path)
            store.save_profile(prof)
            print(_ok(f"Wrote initial Ideal Employer Profile to {prof_path}"))
            return 0
        if profile_exists:
            prof = IdealEmployerProfile.load(prof_path)
        else:
            prof = store.load_profile()
            if prof is None:
                raise FileNotFoundError(
                    f"No profile found at {prof_path} or in the selected database. Run 'career-fleet profile --init'."
                )
            prof.save(prof_path)
        store.save_profile(prof)
    except (OSError, ValueError, sqlite3.Error) as exc:
        print(f"Error: Could not load or store profile {prof_path}: {exc}", file=sys.stderr)
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
    print(f"\nNegative Stack:")
    for s in prof.negative_stack:
        print(f"  {_bullet()} {s}")
    print(f"\nHard Dealbreakers:")
    print(f"  {_bullet()} Max Headcount: {prof.dealbreakers.max_headcount}")
    print(f"  {_bullet()} Verified Headcount: {prof.dealbreakers.require_verified_headcount}")
    print(f"  {_bullet()} Policy:        {prof.dealbreakers.policy}")
    print(f"  {_bullet()} Disallowed:    {', '.join(prof.dealbreakers.disallowed_locations)}")
    print(f"  {_bullet()} Reject Wrapper:{prof.dealbreakers.reject_thin_wrappers}")
    print(f"  {_bullet()} Reject Quota:  {prof.dealbreakers.reject_pure_quota}")
    print(f"  {_bullet()} Candidate TZ:  {prof.dealbreakers.candidate_timezone or '-'}")
    print(f"  {_bullet()} TZ Overlap:    {prof.dealbreakers.min_timezone_overlap_hours}h")
    print(f"\nHiring Catalysts:")
    for c in prof.hiring_catalysts:
        print(f"  {_bullet()} {c}")
    print(f"\nTarget Leadership:")
    for leader in prof.target_leadership:
        print(f"  {_bullet()} {leader}")
    print(f"\nAnchor Exemplars:  {', '.join(prof.anchor_companies)}")
    print("==========================================================================================")
    return 0


def cmd_discover(args):
    store = _open_store(getattr(args, "db", "career_fleet.db"), getattr(args, "workspace_root", "."))
    if store is None:
        return 1
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
    print(_ok(f"Lane 1 Discovery completed: {res['companies_discovered']} companies discovered, {res['postings_added']} source records ingested."))
    if res.get("pages_skipped"):
        print(f"Skipped pages: {res['pages_skipped']}")
    return 0


def cmd_triage(args):
    store = _open_store(getattr(args, "db", "career_fleet.db"), getattr(args, "workspace_root", "."))
    if store is None:
        return 1
    try:
        prof = get_profile(
            getattr(args, "profile", None),
            store=store,
            workspace_root=getattr(args, "workspace_root", "."),
        )
    except (OSError, ValueError, sqlite3.Error) as exc:
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
    store = _open_store(getattr(args, "db", "career_fleet.db"), getattr(args, "workspace_root", "."))
    if store is None:
        return 1
    try:
        prof = get_profile(
            getattr(args, "profile", None),
            store=store,
            workspace_root=getattr(args, "workspace_root", "."),
        )
    except (OSError, ValueError, sqlite3.Error) as exc:
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
    store = _open_store(getattr(args, "db", "career_fleet.db"), getattr(args, "workspace_root", "."))
    if store is None:
        return 1
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
    store = _open_store(getattr(args, "db", "career_fleet.db"), getattr(args, "workspace_root", "."))
    if store is None:
        return 1
    dossier = store.get_company_dossier(args.company)
    if not dossier:
        print(f"Error: Company '{args.company}' not found.", file=sys.stderr)
        return 1

    print("==========================================================================================")
    print(f"                         COMPANY DOSSIER: {dossier['name']} ({dossier['id']})")
    print("==========================================================================================")
    print(f"Domain:      {dossier.get('domain') or '-'}")
    print(f"Headcount:   {dossier.get('headcount') if dossier.get('headcount') is not None else '-'}")
    print(f"HQ:          {dossier.get('hq_location') or '-'}")
    print(f"Timezone:    {dossier.get('timezone') or '-'}")
    print(f"Status:      {dossier.get('status')}")
    if dossier.get("disqualification_reason"):
        print(f"Reason:      {dossier['disqualification_reason']}")
    print(f"Sources:     {len(dossier.get('jobs', []))} source records captured")
    if dossier.get("jobs"):
        print("\nSources:")
        for job in dossier["jobs"]:
            print(f"  [{job['id']}] {job.get('title') or 'Untitled'}")
            print(f"    Source:   {job.get('source_type') or 'manual/legacy'}")
            print(f"    Location: {job.get('location') or '-'}")
            print(f"    URL:      {job.get('job_url') or '-'}")
            if getattr(args, "show_source", False):
                print("    Source text:")
                print(textwrap.indent(job.get("raw_text") or "", "      "))
    print("\nEvaluations:")
    for ev in dossier.get("evaluations", []):
        try:
            quotes = json.loads(ev.get("quotes_json") or "[]")
        except (TypeError, ValueError):
            quotes = []
        print(f"  [{ev['lane']}] Verdict: {ev['verdict']} (Score: {ev['score']}) via {ev.get('model_used') or 'heuristic'}")
        print(f"    Rationale: {ev['rationale']}")
        if quotes:
            print("    Quotes:")
            for q in quotes:
                print(f"      {_bullet()} \"{q}\"")
    print("==========================================================================================")
    return 0


def cmd_export(args):
    store = _open_store(getattr(args, "db", "career_fleet.db"), getattr(args, "workspace_root", "."))
    if store is None:
        return 1
    qualified = [c for c in store.list_companies() if c["status"] == "qualified"]
    dossiers = [store.get_company_dossier(c["id"]) for c in qualified]
    for dossier in dossiers:
        if not dossier:
            continue
        for evaluation in dossier.get("evaluations", []):
            try:
                evaluation["quotes"] = json.loads(evaluation.get("quotes_json") or "[]")
            except (TypeError, ValueError):
                evaluation["quotes"] = []
    out_path = Path(getattr(args, "output", "qualified_targets.json")).expanduser()
    try:
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(json.dumps(dossiers, indent=2), encoding="utf-8")
    except OSError as exc:
        print(f"Error: Could not write export {out_path}: {exc}", file=sys.stderr)
        return 1
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
        description="Deterministic Career & Employer Screening with Multi-Lane Checks.",
    )
    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    p_init = subparsers.add_parser("init", help="Initialize SQLite DB and profile template")
    p_init.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_init.add_argument("--workspace-root", default=".", help="Workspace root for relative paths")
    p_init.set_defaults(func=cmd_init)

    p_prof = subparsers.add_parser("profile", help="Inspect or generate Ideal Employer Profile")
    p_prof.add_argument("--path", default="profile.json", help="Path to profile.json")
    p_prof.add_argument("--db", default="career_fleet.db", help="SQLite database path for the stored profile")
    p_prof.add_argument("--workspace-root", default=".", help="Workspace root for relative paths")
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
    p_trig.add_argument("--profile", default=None, help="Path to profile.json (default: ./profile.json; otherwise use the active IEP in SQLite)")
    p_trig.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_trig.add_argument("--workspace-root", default=".", help="Workspace root for relative paths")
    p_trig.set_defaults(func=cmd_triage)

    p_rec = subparsers.add_parser("recon", help="Lanes 3 & 4: Systems wedge & culture recon")
    p_rec.add_argument("--lane", choices=["systems", "culture", "all"], default="all", help="Which lane to run")
    p_rec.add_argument("--profile", default=None, help="Path to profile.json (default: ./profile.json; otherwise use the active IEP in SQLite)")
    p_rec.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_rec.add_argument("--workspace-root", default=".", help="Workspace root for relative paths")
    p_rec.set_defaults(func=cmd_recon)

    p_list = subparsers.add_parser("list", help="List tracked companies")
    p_list.add_argument("--status", choices=["discovered", "triaged", "qualified", "disqualified"], help="Filter by status")
    p_list.add_argument("--db", default="career_fleet.db", help="SQLite database path")
    p_list.set_defaults(func=cmd_list)

    p_dos = subparsers.add_parser("dossier", help="Inspect company dossier")
    p_dos.add_argument("--company", required=True, help="Company ID")
    p_dos.add_argument("--show-source", action="store_true", help="Print complete captured source text")
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
