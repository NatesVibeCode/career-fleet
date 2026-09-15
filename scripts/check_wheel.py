"""Build and exercise a wheel in a fresh venv, outside the source checkout."""
import argparse
import os
import subprocess
import sys
import tempfile
import venv
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--distribution", choices=("career-fleet",), required=True)
    parser.add_argument("--offline-system-deps", action="store_true", help="Reuse installed dependencies when offline; does not verify dependency installation")
    args = parser.parse_args()
    repository = Path(__file__).resolve().parents[1]
    with tempfile.TemporaryDirectory(prefix="fleet wheel check ") as directory:
        root = Path(directory)
        wheel_dir = root / "wheels"
        build_options = ["--no-build-isolation", "--no-index"] if args.offline_system_deps else []
        subprocess.run([sys.executable, "-m", "pip", "wheel", str(repository), "--no-deps", "--wheel-dir", str(wheel_dir), *build_options], check=True)
        environment = root / "venv"
        venv.EnvBuilder(with_pip=True, system_site_packages=args.offline_system_deps).create(environment)
        bindir = environment / ("Scripts" if os.name == "nt" else "bin")
        python = bindir / ("python.exe" if os.name == "nt" else "python")
        wheel, = wheel_dir.glob("*.whl")
        install_options = ["--no-deps", "--no-index", "--ignore-installed"] if args.offline_system_deps else []
        subprocess.run([str(python), "-m", "pip", "install", str(wheel), *install_options], check=True)
        workspace = root / "sales workspace"
        workspace.mkdir()
        env = {key: value for key, value in os.environ.items() if not key.startswith((
            "PYTHONPATH", "HARNESS_FLEET", "OPENROUTER", "OPENAI", "OLLAMA", "LMSTUDIO", "VLLM", "GROQ", "CEREBRAS", "OPENCODE",
        ))}
        cli = bindir / (args.distribution + (".exe" if os.name == "nt" else ""))
        subprocess.run([str(python), "-c", "import harness_fleet, sys; from pathlib import Path; assert Path(harness_fleet.__file__).is_relative_to(Path(sys.prefix)), harness_fleet.__file__; assert (Path(harness_fleet.__file__).parent / 'resources' / 'studio' / 'index.html').is_file(), 'studio page missing from wheel'"], cwd=workspace, env=env, check=True)

        if args.distribution == "career-fleet":
            subprocess.run([str(python), "-c", "import career_fleet, sys; from pathlib import Path; assert Path(career_fleet.__file__).is_relative_to(Path(sys.prefix)), career_fleet.__file__"], cwd=workspace, env=env, check=True)
            run_help = subprocess.run([str(cli), "--help"], cwd=workspace, env=env, capture_output=True, text=True)
            assert run_help.returncode == 0, run_help.stderr
            run_init = subprocess.run([str(cli), "init"], cwd=workspace, env=env, capture_output=True, text=True)
            assert run_init.returncode == 0, run_init.stderr
            assert (workspace / "career_fleet.db").is_file()
            assert (workspace / "profile.json").is_file()
            run_prof = subprocess.run([str(cli), "profile"], cwd=workspace, env=env, capture_output=True, text=True)
            assert run_prof.returncode == 0, run_prof.stderr
            assert "IDEAL EMPLOYER PROFILE" in run_prof.stdout
            run_setup = subprocess.run([str(cli), "setup", "--workspace-root", str(workspace)], cwd=workspace, env=env, capture_output=True, text=True)
            assert run_setup.returncode == 0, run_setup.stderr
            assert (workspace / ".agents/skills/career-fleet/SKILL.md").is_file()
            mode = "reused system dependencies" if args.offline_system_deps else "fresh dependencies"
            print(f"career-fleet: installed wheel setup, profile init, skill install, and CLI passed ({mode})")
            return


if __name__ == "__main__":
    main()
