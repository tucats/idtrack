# Backup Management Strategies

When a new backup is generated — because the server has restarted or because the backup interval has elapsed — a fresh copy of the database file is always written to the backup directory first. After the copy is complete, the server applies any enabled thinning strategies in the following fixed order:

1. **Size-based thinning** — keep total backup storage within a disk-space limit
2. **Count-based thinning** — keep total number of backup files within a count limit
3. **Age-based thinning** — remove backups whose age exceeds a maximum age

Each strategy is independent. You can enable any combination of the three. Each one is applied only when its corresponding threshold is set to a non-zero value.

The strategies are configured with these `idtrack default` flags:

| Flag | Argument | Description |
| --- | --- | --- |
| `--backup-interval` | duration | How often backups are made (`1h`, `30m`, etc.). Set to `0` or `off` to disable backups entirely. |
| `--backup-size` | size | Maximum total disk space used by all backup files combined (`500mb`, `2gb`, etc.). |
| `--backup-count` | number | Maximum number of backup files to retain. |
| `--backup-age` | duration | Maximum age of a backup file (`168h` = one week, etc.). |

---

## Enabling Backups

Backups are disabled by default. Set `--backup-interval` to a non-zero duration to enable them:

```sh
idtrack default --backup-interval 1h
```

A backup is written each time the interval elapses, and also immediately when the server starts. Backup files are written to an `idtrack-backups/` directory placed alongside the database file. The directory is created automatically if it does not exist.

Backup filenames embed a UTC timestamp:

```text
idtrack-20260517T143000.db
```

Alphabetical order equals chronological order for this naming scheme, which simplifies listing, pruning, and identifying the most recent backup. The embedded timestamp — not the filesystem modification time — is used as the authoritative age source for all thinning decisions.

To disable backups:

```sh
idtrack default --backup-interval off
```

---

## How Backups are Written

While a backup copy is in progress the server briefly quiesces new incoming requests using an RWMutex write lock so the database file is stable for the duration of the copy. In-flight requests complete normally; any requests that arrive while the copy is running wait a fraction of a second and then proceed without error. No data is lost and clients see no visible interruption.

The copy is performed with `io.Copy` followed by an explicit `fsync` before the destination file is closed, ensuring the backup data reaches disk before the backup is considered complete.

---

## 1. Size-Constrained Thinning

```sh
idtrack default --backup-size SIZE
```

After each backup, if the total disk space used by all backup files exceeds `SIZE`, older backups are thinned until the total is within the limit — or until no further thinning is possible without deleting the only remaining backup.

### Expressing the size

`SIZE` is a number followed by an optional unit suffix (case-insensitive):

| Suffix | Meaning |
| ------ | ------- |
| `b` | bytes |
| `kb` | kilobytes (1,024 bytes) |
| `mb` | megabytes (1,024 KB) |
| `gb` | gigabytes (1,024 MB) |
| `tb` | terabytes (1,024 GB) |

Decimal values are accepted: `0.5gb` and `.5gb` both mean 512 MB. If no suffix is given, the value is treated as bytes.

```sh
idtrack default --backup-size 500mb
idtrack default --backup-size 2gb
idtrack default --backup-size 1.5tb
idtrack default --backup-size off   # disable size thinning
```

### Density-aware thinning algorithm

Size thinning does not simply delete the oldest files. Instead it uses a **Time Machine-style density algorithm** that preserves high-resolution recent backups and thins out older ones.

**Density rules (preserved by the algorithm):**

1. **Last hour** — every backup file made within the last hour is kept.
2. **Previous 23 hours** — at most one backup per hour is kept (the most recent within each 1-hour window).
3. **Older backups** — at most one backup per day is kept (the most recent within each 24-hour window).

**Thinning priority order** (candidates are deleted in this order until the size limit is met or no further thinning is possible):

1. **Extra files in hourly buckets.** Within each 1-hour window (ages 1–23 hours), all but the most recent backup are candidates. The newest hourly bucket (1–2 hours old) is cleared first, working toward the oldest (22–23 hours old). Within each bucket, the oldest files are deleted first.

2. **Extra files in daily buckets.** Within each 24-hour window (ages beyond 24 hours), all but the most recent backup are candidates. The newest daily bucket is cleared first; within each, the oldest files are deleted first.

3. **Hourly-to-daily bridge.** The backup in the 23rd-hour bucket (23–24 hours old) is the next to age into the daily zone. If a daily backup already exists covering that day, the 23rd-hour backup is preemptively removed — it will become redundant on the very next thinning pass anyway.

4. **Oldest daily keeper.** If the size limit still is not met, the oldest day's sole remaining backup is deleted. This is repeated for successively newer days until the limit is satisfied or only the most recent daily backup remains.

The model is inspired by Apple's Time Machine. Unlike Time Machine, idtrack cannot merge multiple snapshots into one — it can only delete whole files — so thinning always selects the least-informative files first within each density tier.

---

## 2. Count-Constrained Thinning

```sh
idtrack default --backup-count N
```

After each backup (and after any size thinning has run), if the total number of backup files still exceeds `N`, the oldest files are deleted until exactly `N` remain. The embedded filename timestamp determines file age.

Use `0` or `off` for no count limit:

```sh
idtrack default --backup-count off
```

Count thinning is simple and predictable but has no awareness of how the backups are distributed in time — it just keeps the N most recent files regardless of their age spread.

---

## 3. Age-Constrained Thinning

```sh
idtrack default --backup-age DURATION
```

After each backup (and after size and count thinning have run), any backup file whose name-embedded timestamp is older than `now − DURATION` is deleted. The duration is any Go duration string, for example `168h` (7 days) or `720h` (30 days).

Because Go duration strings do not have a "days" or "weeks" unit, use multiples of hours: `48h` = 2 days, `168h` = 1 week, `720h` = 30 days.

Use `0` or `off` to disable age-based thinning:

```sh
idtrack default --backup-age off
```

---

## Combining Strategies

All three strategies can be active simultaneously. Example — create a backup every hour, cap total storage at 2 GB, keep at most 48 files, and discard anything older than 7 days:

```sh
idtrack default \
  --backup-interval 1h   \
  --backup-size    2gb   \
  --backup-count   48    \
  --backup-age     168h
```

After each backup the server runs size thinning, then count thinning, then age thinning — in that order — so the most space-efficient strategy (density-aware size thinning) always runs before the cruder count and age cutoffs.

---

## Restoring from a Backup

1. Stop the server: `idtrack stop`
2. Replace the live database file with the chosen backup:
   ```sh
   cp /path/to/idtrack-backups/idtrack-20260517T120000.db /path/to/idtrack.db
   ```
3. Restart the server: `idtrack serve`

Backup files are complete, self-contained SQLite databases. They can be opened with any SQLite-compatible tool for inspection or data recovery without restoring them to the live location first.
