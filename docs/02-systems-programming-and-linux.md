# 02 — Systems Programming & Linux Internals

This document details the low-level operating system mechanics, Linux kernel interfaces, FreeDesktop XDG specifications, and VFS concepts used by the Go systems core.

---

## 1. Directory Traversal: `os.Lstat` vs `os.Stat`

When traversing a Linux filesystem tree, choosing the right stat syscall is critical for correctness and security:

```
                  ┌──────────────────────┐
                  │ Target Path / Entry  │
                  └──────────┬───────────┘
                             │
            ┌────────────────┴────────────────┐
            ▼                                 ▼
   ┌─────────────────┐               ┌─────────────────┐
   │    os.Stat()    │               │   os.Lstat()    │
   └────────┬────────┘               └────────┬────────┘
            │                                 │
     Dereferences Symlink             Inspects Inode Directly
     (Follows Pointer Target)         (Does NOT Dereference)
            │                                 │
   ⚠️ Risk: Circular Symlinks         ✅ Safe: Detects Symlinks
   lead to Infinite Loops & OOM       via (mode & ModeSymlink != 0)
```

- **`os.Stat(path)`**: Executes the `stat()` syscall. If `path` is a symbolic link, the Linux kernel resolves the target recursively until the final destination is reached. If user folders contain circular links (`dirA/link -> dirA`), recursive scanners become trapped in infinite loops.
- **`os.Lstat(path)`**: Executes the `lstat()` syscall. If `path` is a symbolic link, `lstat` returns information about the link file itself, without following it.
- **Our Implementation**: We invoke `os.Lstat()` on every directory entry. If `info.Mode() & os.ModeSymlink != 0`, we skip traversal, protecting against symlink race conditions and loops.

---

## 2. Inode Extraction & Hardlink Deduplication via `syscall.Stat_t`

Go's standard `os.FileInfo` interface is designed to be cross-platform, hiding OS-specific fields. However, on Linux systems, the underlying implementation exposes POSIX `struct stat` via the `Sys()` method.

```go
if stat, ok := info.Sys().(*syscall.Stat_t); ok {
    inode = uint64(stat.Ino)
    atime = time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
    mtime = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
}
```

### Inodes (`stat.Ino`) & Hardlink Detection
- An Inode (Index Node) describes a physical filesystem object on disk.
- **Hardlinks**: Multiple file paths sharing the exact same Inode number point to the exact same physical disk blocks.
- **Deduplication Logic**: When two identical files are identified, the engine compares their Inodes. If `InodeA == InodeB`, they are recognized as hardlinks and are **not** counted as wasted redundant disk space.

### Access Time (`atime`) vs Modification Time (`mtime`)
- **`mtime` (`stat.Mtim`)**: The timestamp when file contents were last modified (updated on `write()`, `truncate()`).
- **`atime` (`stat.Atim`)**: The timestamp when the file was last read (`read()`, `execve()`).
- **Linux `relatime` Mount Option**: Modern Linux kernels only update `atime` if it is older than `mtime` or after 24 hours. The staleness engine uses `mtime` as the primary ground truth and `atime` as a supplementary indicator.

---

## 3. System Storage Classification & OS Safety

Linux organizes system-level data across well-defined root hierarchies. During directory traversal, every path is parsed and classified into one of 6 core categories:

| Category | Typical Linux Paths | Nature & Lifecycle | Safety Rule |
| :--- | :--- | :--- | :--- |
| `system_protected` | `/etc`, `/usr`, `/boot`, `/lib`, `/bin`, `/opt` | Critical operating system binaries and configuration | **STRICTLY PROTECTED**: Staleness weight = `0.01`, cannot be deleted. |
| `system_log` | `/var/log`, `/var/adm`, `*.log`, `*.syslog` | Diagnostic logs, journald archives, syslog files | **CLEANUP CANDIDATE**: Staleness boost = `1.30`. |
| `crash_dump` | `/var/crash`, `/var/lib/systemd/coredump`, `*.core`, `*.dmp` | Application crash memory dumps | **HIGH-VALUE CLEANUP**: Staleness boost = `1.40`. |
| `temp` | `/tmp`, `/var/tmp`, `/dev/shm`, `*.lock`, `*.tmp` | Ephemeral sockets, locks, runtime scratchpads | **HIGH-VALUE CLEANUP**: Staleness boost = `1.35`. |
| `system_cache` | `/var/cache`, `/var/spool`, `/root/.cache` | APT/Pacman packages, font caches, thumbnails | **MODERATE CLEANUP**: Staleness boost = `1.15`. |
| `user` | `/home/...`, `/media/...`, `/mnt/...` | User documents, developer repositories, personal media | Analyzed according to standard decay rules. |

---

## 4. Avoiding File Descriptor Exhaustion (`EMFILE`)

Every open file or directory in Linux consumes an OS File Descriptor (FD). Linux enforces a per-process limit (`ulimit -n`, typically 1024).

- **Unbounded Recursion**: Spawning a new goroutine for every directory opens thousands of FDs simultaneously, crashing with `EMFILE: too many open files`.
- **Bounded Pool**: Our scanner maintains a fixed pool of worker goroutines (`runtime.NumCPU()`). Each worker opens a directory, calls `Readdirnames()`, closes the FD immediately, and feeds child directories back into a bounded work channel. Peak open file descriptors never exceed $O(\text{workers})$.

---

## 5. Streaming SHA-256 Hashing & Memory Bounded I/O

Computing cryptographic hashes for multi-gigabyte files (e.g. 10 GB videos, ISOs, virtual disks) must never load full or partial files into main memory (RAM):

```
[Large File on Disk (e.g., 10 GB)]
               │
               ▼ (Reads 64 KB Chunks into RAM)
 ┌───────────────────────────┐
 │   Fixed 64 KB RAM Buffer  │  <-- Constant Memory Overhead per Worker
 └─────────────┬─────────────┘
               │
               ▼ (Feeds into SHA-256 Compression Engine)
 ┌───────────────────────────┐
 │  SHA-256 Engine Loop      │  <-- Ingests 64-byte (512-bit) sub-blocks
 │  (Hardware Accelerated)   │      Continually updates internal state registers
 └─────────────┬─────────────┘
               │
               ▼ (Buffer re-used for next 64 KB chunk until EOF)
 ┌───────────────────────────┐
 │  Final Hex Digest         │  <-- Single 64-character hex string (256 bits)
 └───────────────────────────┘
```

### Detailed Mechanics:
1. **Bounded 64 KB Buffer**: Each worker allocates exactly one 64 KB byte buffer.
2. **Streaming Execution**: `io.CopyBuffer(hasher, file, buf)` reads 64 KB from disk, passes it to `hasher.Write()`, and immediately overwrites the buffer with the next chunk.
3. **Internal SHA-256 64-Byte Block Processing**: Inside the crypto library, data is consumed in 64-byte blocks (512 bits) to update the 256-bit internal state registers.
4. **Collision Resistance**: SHA-256 incorporates exact byte sequences and data length. Different byte counts mathematically cannot generate identical hashes.
5. **Universal Format Support**: All files (audio, video, archives, images, text) are processed identically as binary raw byte streams.

---

## 6. Linux Native Trash Integration (FreeDesktop.org XDG Spec)

To ensure deleted files appear in the user's native Linux desktop trash bin (GNOME Nautilus, KDE Dolphin, Ubuntu Trash), the system adheres to the FreeDesktop.org Trash Specification:

```
~/.local/share/Trash/
├── files/                      <-- Physical file relocated here
│   └── document.pdf
└── info/                       <-- FreeDesktop metadata descriptor
    └── document.pdf.trashinfo
```

### Format of `.trashinfo` File:
```ini
[Trash Info]
Path=/home/user/Documents/document.pdf
DeletionDate=2026-08-14T22:30:00
```
This enables native OS-level inspection, emptying, and restoration directly from the Linux desktop UI.
