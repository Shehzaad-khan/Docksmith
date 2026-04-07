\# Docksmith



> \*\*A simplified Docker-like build and runtime system built entirely from scratch.\*\*



Docksmith is designed to demonstrate how build caching, content-addressing, process isolation, and image assembly work under the hood. It operates strictly as a single CLI binary without any background daemon processes.



\---



\## Scope \& Constraints



\- \*\*Fully Offline:\*\* No network access during build or run. Base images must be downloaded and imported beforehand.

\- \*\*OS-Level Isolation:\*\* Uses native Linux primitives for process isolation instead of existing tools like `runc` or `containerd`.

\- \*\*Out of Scope:\*\*

&#x20; - Networking

&#x20; - Image registries

&#x20; - Resource limits

&#x20; - Multi-stage builds

&#x20; - Bind mounts

&#x20; - Detached containers

&#x20; - Daemon processes



\---



\## Architecture \& State



All Docksmith state is stored locally on disk in the user's home directory.



\### Directory Layout



```text

\~/.docksmith/

├── images/     # One JSON manifest per image

├── layers/     # Content-addressed tar files named by SHA-256 digest

└── cache/      # Index mapping cache keys to layer digests

```



\---



\## Supported Docksmithfile Instructions



The build engine parses a file named `Docksmithfile`. It strictly supports the following 6 instructions:



| Instruction | Behavior |

|---|---|

| `FROM <image>\[:<tag>]` | Uses local layers as the base filesystem. Fails if not found locally. |

| `COPY <src> <dest>` | Copies files into the image, supporting `\*` and `\*\*` globs. Produces a layer. |

| `RUN <command>` | Executes a shell command inside the assembled filesystem in strict isolation. Produces a layer. |

| `WORKDIR <path>` | Sets the working directory for subsequent instructions. Updates config only. |

| `ENV <key>=<value>` | Stores environment variables injected during build and runtime. Updates config only. |

| `CMD \["exec", "arg"]` | Sets the default command on container start. Requires JSON array form. Updates config only. |



\---



\## CLI Reference



| Command | Description |

|---|---|

| `docksmith build -t <name:tag> <context>` | Parses Docksmithfile, executes steps in isolation, writes manifest, and logs cache status. |

| `docksmith build --no-cache ...` | Skips all cache lookups and writes for the build. |

| `docksmith images` | Lists all local images (Columns: Name, Tag, ID, Created). |

| `docksmith run <name:tag> \[cmd]` | Assembles filesystem, starts container in foreground, and waits for exit. |

| `docksmith run -e KEY=VALUE ...` | Overrides or adds an environment variable at runtime. |

| `docksmith rmi <name:tag>` | Removes the image manifest and all associated layer files from disk. |



\---



\## Prerequisites \& Setup



\### System Requirements



\- Linux OS is strictly required for native process isolation.

\- Windows and macOS users must use a Linux VM (e.g., VirtualBox, VMware, UTM).



\### Clone the Repository



```bash

git clone https://github.com/yourusername/docksmith.git

cd docksmith

```



\### Initialize Local State



Run the CLI once to automatically generate the `\~/.docksmith/` directory structure:



```bash

./docksmith --help

```



\### Import Base Image



Download a minimal root filesystem (e.g., Alpine Linux `rootfs.tar.gz`) for your VM's architecture (`x86\_64` or `ARM64`) and place it in the local store before building.



\---



\## The Team



| Name | Role |

|---|---|

| Mohammed Shehzaad Khan | CLI \& State Architecture |

| Mohammed Mir Fazlai Ali | Build Engine \& Parser |

| Mohammed Affan Khan | Layering \& Cache Systems |

| Mahammed Aahil Parson | Runtime \& OS Isolation |

