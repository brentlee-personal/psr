# Reproducible source baseline (BRE-199)

Goal: get an exact checkout of the pinned RAGFlow tag+commit. **Does not** start Colima/ARM64 full stack (that is BRE-200).

## Pin

See `psr/UPSTREAM.lock.json`:

- tag: `v0.27.3`
- commit: `8090ffd8ba6098813d7f7ec2c4b8774f36c8423a`

## From the private mirror

```bash
git clone https://github.com/brentlee-personal/psr.git
cd psr
git fetch origin tag v0.27.3
git checkout -B psr/baseline-v0.27.3 v0.27.3
test "$(git rev-parse HEAD)" = "8090ffd8ba6098813d7f7ec2c4b8774f36c8423a"
```

## From upstream directly

```bash
git clone https://github.com/infiniflow/ragflow.git
cd ragflow
git fetch origin tag v0.27.3
git checkout --detach v0.27.3
test "$(git rev-parse HEAD)" = "8090ffd8ba6098813d7f7ec2c4b8774f36c8423a"
```

## Remotes expected on the controlled mirror

```bash
git remote add upstream https://github.com/infiniflow/ragflow.git   # if missing
git remote -v
# origin   → brentlee-personal/psr
# upstream → infiniflow/ragflow
```

## Verify license files intact

```bash
test -f LICENSE
# Root NOTICE is absent upstream at this pin; do not fabricate one.
```
