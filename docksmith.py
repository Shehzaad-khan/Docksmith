import argparse
import json
import sys
from pathlib import Path

# ==========================================
# TASK 2: Local State Initialization
# ==========================================
def init_state_dirs():
    """
    Ensures the ~/.docksmith/ directory and its required subdirectories exist.
    This runs every time the CLI is invoked.
    """
    base_dir = Path.home() / ".docksmith"
    
    # Define the required subdirectories
    dirs_to_create = [
        base_dir / "images",  # Stores JSON manifests
        base_dir / "layers",  # Stores content-addressed tar files
        base_dir / "cache"    # Stores the cache index
    ]
    
    # Create them if they do not exist
    for directory in dirs_to_create:
        directory.mkdir(parents=True, exist_ok=True)
        
    return base_dir


# ==========================================
# COMMAND HANDLERS (Stubs for now)
# ==========================================
def cmd_build(args):
    """Placeholder for Member 2 (Build Engine) and Member 3 (Cache)."""
    print(f"[Build Engine] Building image '{args.tag}' from context '{args.context}'")
    if args.no_cache:
        print("[Build Engine] Notice: --no-cache flag detected. Skipping cache.")

def cmd_images(args):
    images_dir = Path.home() / ".docksmith" / "images"
    manifests = sorted(images_dir.glob("*.json"))

    if not manifests:
        print("No images found.")
        return

    rows = []
    for manifest_path in manifests:
        with open(manifest_path) as f:
            data = json.load(f)
        image_id = data["digest"].replace("sha256:", "")[:12]
        rows.append((data["name"], data["tag"], image_id, data["created"]))

    print(f"{'NAME':<20} {'TAG':<15} {'IMAGE ID':<12} {'CREATED'}")
    for name, tag, image_id, created in rows:
        print(f"{name:<20} {tag:<15} {image_id:<12} {created}")

def cmd_run(args):
    """Placeholder for Member 4 (Runtime & Isolation)."""
    print(f"[Runtime] Assembling filesystem and running image '{args.image}'")
    if args.e:
        print(f"[Runtime] Environment Overrides applied: {args.e}")
    if args.cmd:
        print(f"[Runtime] Command Override applied: {' '.join(args.cmd)}")

def cmd_rmi(args):
    images_dir = Path.home() / ".docksmith" / "images"
    layers_dir = Path.home() / ".docksmith" / "layers"

    if ":" in args.image:
        name, tag = args.image.split(":", 1)
    else:
        name, tag = args.image, "latest"

    manifest_path = images_dir / f"{name}_{tag}.json"
    if not manifest_path.exists():
        print(f"Error: image '{args.image}' not found.", file=sys.stderr)
        sys.exit(1)

    with open(manifest_path) as f:
        manifest = json.load(f)

    for layer in manifest["layers"]:
        layer_file = layers_dir / layer["digest"]
        if layer_file.exists():
            layer_file.unlink()

    manifest_path.unlink()
    print(f"Untagged: {args.image}")


# ==========================================
# TASK 1: The Entry Point (CLI Parser)
# ==========================================
def main():
    # 1. Always initialize state directories first
    init_state_dirs()
    
    # 2. Setup the main argument parser
    parser = argparse.ArgumentParser(
        prog="docksmith",
        description="Docksmith: A simplified Docker-like build and runtime system."
    )
    
    # Create sub-commands (build, images, run, rmi)
    subparsers = parser.add_subparsers(title="commands", dest="command", required=True)
    
    # --- Command: docksmith build ---
    parser_build = subparsers.add_parser("build", help="Build an image from a Docksmithfile")
    parser_build.add_argument("-t", "--tag", required=True, help="Name and optionally a tag (name:tag)")
    parser_build.add_argument("context", help="Path to the build context directory")
    parser_build.add_argument("--no-cache", action="store_true", help="Skip all cache lookups and writes")
    parser_build.set_defaults(func=cmd_build)
    
    # --- Command: docksmith images ---
    parser_images = subparsers.add_parser("images", help="List all images in the local store")
    parser_images.set_defaults(func=cmd_images)
    
    # --- Command: docksmith run ---
    parser_run = subparsers.add_parser("run", help="Run a command in a new container")
    parser_run.add_argument("-e", action="append", metavar="KEY=VALUE", help="Override or add an environment variable")
    parser_run.add_argument("image", help="The image to run (name:tag)")
    parser_run.add_argument("cmd", nargs=argparse.REMAINDER, help="Override the default CMD")
    parser_run.set_defaults(func=cmd_run)
    
    # --- Command: docksmith rmi ---
    parser_rmi = subparsers.add_parser("rmi", help="Remove an image manifest and its layers")
    parser_rmi.add_argument("image", help="The image to remove (name:tag)")
    parser_rmi.set_defaults(func=cmd_rmi)
    
    # 3. Parse the arguments and trigger the appropriate function
    args = parser.parse_args()
    args.func(args)

if __name__ == "__main__":
    main()