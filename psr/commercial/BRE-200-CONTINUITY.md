# BRE-200 continuity after BRE-206 merge

Do **not** recreate/stop `ragflow-psr-m05-*` for BRE-206.

When commercial compose defaults land on the branch BRE-200 uses, keep BRE-200 on
MySQL + silo until acceptance finishes:

- `DOC_ENGINE=infinity`
- `DB_TYPE=mysql`
- `METADATA_DB_PROFILE=mysql`
- `OBJECT_STORE_PROFILE=silo-agpl`
- `STORAGE_IMPL=MINIO`
- `COMPOSE_PROFILES=infinity,cpu,metadata-mysql,object-silo-agpl,tei-cpu` (adjust TEI as already used)

Host ports must continue to avoid G0 `:5432` / `:8787`.

Cut over to postgres + SeaweedFS only after BRE-200 PASS and 主理批.
