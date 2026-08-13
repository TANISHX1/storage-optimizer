# 02 — Systems Programming & Linux Internals

This document details the low-level operating system mechanics, Linux kernel interfaces, and VFS concepts used by the Go systems core.

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

## 2. Inode Extraction & Timestamps via `syscall.Stat_t`

Go's standard `os.FileInfo` interface is designed to be cross-platform, hiding OS-specific fields. However, on Linux systems, the underlying implementation exposes POSIX `struct stat` via the `Sys()` method.

```go
if stat, ok := info.Sys().(*syscall.Stat_t); ok {
    inode = uint64(stat.Ino)
    atime = time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
    mtime = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
}
```

### Inodes (`stat.Ino`)
- An Inode (Index Node) is a data structure on Unix-style filesystems that describes a filesystem object (file or directory).
- **Hard Links**: Two different file paths sharing the exact same Inode number refer to the exact same physical blocks on disk. By recording Inodes, our duplicate detector avoids falsely flagging hard links as wasted redundant space.

### Access Time (`atime`) vs Modification Time (`mtime`)
- **`mtime` (`stat.Mtim`)**: The timestamp when the content of the file was last modified. This is strictly updated by write operations (`write()`, `truncate()`).
- **`atime` (`stat.Atim`)**: The timestamp when the file was last read (`read()`, `execve()`).
- **Linux `relatime` Mount Option**: Modern Linux distributions mount filesystems with `relatime` (relative access time) by default. The kernel only updates `atime` on disk if the previous `atime` is older than `mtime` or has not been updated within 24 hours. Because of `relatime` or `noatime`, our staleness engine weights `mtime` as the primary reliable signal and `atime` as a supplementary indicator.

---

## 3. Avoiding File Descriptor Exhaustion (`EMFILE`)

Every open file or directory in Linux consumes an OS File Descriptor (FD). Linux enforces a per-process limit (defaulting to 1024 on many distributions, inspectable with `ulimit -n`).

- **Unbounded Recursion**: Spawning a new goroutine for every directory causes thousands of open directories simultaneously, instantly crashing with `EMFILE: too many open files`.
- **Bounded Pool**: Our scanner maintains a fixed pool of worker goroutines (`runtime.NumCPU()`). Each worker opens a directory, calls `Readdirnames()`, closes the FD immediately, and feeds child directories back into a bounded work channel. Peak open file descriptors never exceed $O(\text{workers})$.

---

## 4. Streaming SHA-256 Hashing for Large Files

Computing content hashes for large files (e.g., 10 GB disk images or 4K videos) must never load the entire file into user-space memory:

```
[Large File on Disk (e.g., 4 GB)]
               │
               ▼ (64 KB Chunks via io.CopyBuffer)
 ┌───────────────────────────┐
 │   Fixed 64 KB RAM Buffer  │  <-- Constant Memory Overhead
 └─────────────┬─────────────┘
               ▼
 ┌───────────────────────────┐
 │   crypto/sha256 Hasher    │  <-- Incremental hash.Write()
 └─────────────┬─────────────┘
               ▼
 ┌───────────────────────────┐
 │  32-byte Hex Digest       │
 └───────────────────────────┘
```

- We stream files in 64 KB fixed chunks directly into `crypto/sha256.New()`. Memory consumption remains bounded at $O(\text{workers} \times 64\text{ KB})$, even when hashing terabytes of data.
