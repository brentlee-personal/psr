from collections.abc import Iterable
from pathlib import Path
from typing import Any

from .http_client import HttpClient

try:
    from requests_toolbelt import MultipartEncoder
except ImportError:  # pragma: no cover - fallback without toolbelt
    MultipartEncoder = None


class DatasetError(RuntimeError):
    pass


def create_dataset(client: HttpClient, name: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    body = dict(payload or {})
    if "name" not in body:
        body["name"] = name
    res = client.request_json("POST", "/datasets", json_body=body)
    if res.get("code") != 0:
        raise DatasetError(f"Create dataset failed: {res.get('message')}")
    return res.get("data", {})


def list_datasets(client: HttpClient, dataset_id: str | None = None, name: str | None = None) -> list[dict[str, Any]]:
    params = {}
    if dataset_id is not None:
        params["id"] = dataset_id
    if name is not None:
        params["name"] = name
    res = client.request_json("GET", "/datasets", params=params or None)
    if res.get("code") != 0:
        raise DatasetError(f"List datasets failed: {res.get('message')}")
    return res.get("data", [])


def delete_dataset(client: HttpClient, dataset_id: str) -> None:
    payload = {"ids": [dataset_id]}
    res = client.request_json("DELETE", "/datasets", json_body=payload)
    if res.get("code") != 0:
        raise DatasetError(f"Delete dataset failed: {res.get('message')}")


def upload_documents(client: HttpClient, dataset_id: str, file_paths: Iterable[str]) -> list[dict[str, Any]]:
    paths = [Path(p) for p in file_paths]
    if MultipartEncoder is None:
        files = [("file", (p.name, p.open("rb"))) for p in paths]
        try:
            response = client.request(
                "POST",
                f"/datasets/{dataset_id}/documents",
                headers=None,
                data=None,
                json_body=None,
                files=files,
                params=None,
                stream=False,
                auth_kind="api",
            )
        finally:
            for _, (_, fh) in files:
                fh.close()
        res = response.json()
    else:
        fields = []
        file_handles = []
        try:
            for path in paths:
                fh = path.open("rb")
                fields.append(("file", (path.name, fh)))
                file_handles.append(fh)
            encoder = MultipartEncoder(fields=fields)
            headers = {"Content-Type": encoder.content_type}
            response = client.request(
                "POST",
                f"/datasets/{dataset_id}/documents",
                headers=headers,
                data=encoder,
                json_body=None,
                params=None,
                stream=False,
                auth_kind="api",
            )
            res = response.json()
        finally:
            for fh in file_handles:
                fh.close()
    if res.get("code") != 0:
        raise DatasetError(f"Upload documents failed: {res.get('message')}")
    return res.get("data", [])


def parse_documents(client: HttpClient, dataset_id: str, document_ids: list[str]) -> dict[str, Any]:
    payload = {"document_ids": document_ids}
    res = client.request_json("POST", f"/datasets/{dataset_id}/chunks", json_body=payload)
    if res.get("code") != 0:
        raise DatasetError(f"Parse documents failed: {res.get('message')}")
    return res


def list_documents(client: HttpClient, dataset_id: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
    res = client.request_json("GET", f"/datasets/{dataset_id}/documents", params=params)
    if res.get("code") != 0:
        raise DatasetError(f"List documents failed: {res.get('message')}")
    return res.get("data", {})


def wait_for_parse_done(
    client: HttpClient,
    dataset_id: str,
    document_ids: list[str] | None,
    timeout: float,
    interval: float,
) -> None:
    import time

    start = time.monotonic()
    while True:
        target_ids = set(document_ids or [])
        documents = {}
        page = 1
        while True:
            data = list_documents(client, dataset_id, {"page": page, "page_size": 100, "orderby": "name", "desc": False})
            docs = data.get("docs", [])
            if not docs:
                break
            for doc in docs:
                identity = doc.get("id")
                if not identity or identity in documents:
                    raise DatasetError("Invalid or duplicate document in paginated listing")
                documents[identity] = doc
            if len(documents) >= data.get("total", float("inf")):
                break
            page += 1
        expected = target_ids or set(documents)
        if expected and expected.issubset(documents) and all(documents[identity].get("run") == "DONE" for identity in expected):
            return
        if any(documents[identity].get("run") == "FAIL" for identity in expected.intersection(documents)):
            raise DatasetError("Document parsing failed")
        if time.monotonic() - start > timeout:
            raise DatasetError("Document parsing timeout")
        time.sleep(max(interval, 0.1))


def extract_document_ids(documents: Iterable[dict[str, Any]]) -> list[str]:
    return [doc["id"] for doc in documents if "id" in doc]


def dataset_has_chunks(dataset_info: dict[str, Any]) -> bool:
    for key in ("chunk_count", "chunk_num"):
        value = dataset_info.get(key)
        if isinstance(value, int) and value > 0:
            return True
    return False
