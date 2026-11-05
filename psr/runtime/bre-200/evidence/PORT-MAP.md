# Port map (ragflow-psr-m05) — all 127.0.0.1

| Service | Host port | Container |
| --- | --- | --- |
| ragflow web | 19680 | 80 |
| ragflow https | 19681 | 443 |
| ragflow API | 19682 | 9380 |
| admin | 19683 | 9381 |
| mcp | 19684 | 9382 |
| elasticsearch | 19200 | 9200 |
| mysql | 23306 | 3306 |
| redis | 26379 | 6379 |
| minio | 29000/29001 | 9000/9001 |
| tei | 16380 | 80 |
| infinity (stopped in final smoke) | 23817/23820/25432 | thrift/http/psql |

**Not used:** host `5432`, `8787` (reserved for PSR G0).
