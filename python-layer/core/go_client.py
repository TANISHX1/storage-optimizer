from typing import Any

import requests


class GoCoreAPIError(RuntimeError):
    """Raised when communication with Go Core fails."""


class GoCoreClient:
    """
    Low-level HTTP client for the Go Core REST API.

    Responsibilities:
    - HTTP transport
    - timeouts
    - status/error handling
    - JSON decoding

    It does NOT convert responses into Python domain models.
    That responsibility belongs to GoCoreProvider.
    """

    def __init__(
        self,
        base_url: str = "http://127.0.0.1:8080/api/v1",
        timeout: float = 30.0,
    ):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    # =========================================================
    # HTTP helpers
    # =========================================================

    def get(
        self,
        endpoint: str,
        params: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        return self._request(
            method="GET",
            endpoint=endpoint,
            params=params,
        )

    def post(
        self,
        endpoint: str,
        payload: dict[str, Any] | None = None,
        params: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        return self._request(
            method="POST",
            endpoint=endpoint,
            params=params,
            json=payload,
        )

    def _request(
        self,
        method: str,
        endpoint: str,
        params: dict[str, Any] | None = None,
        json: dict[str, Any] | None = None,
    ) -> dict[str, Any]:

        url = (
            f"{self.base_url}/"
            f"{endpoint.lstrip('/')}"
        )

        try:
            response = requests.request(
                method=method,
                url=url,
                params=params,
                json=json,
                timeout=self.timeout,
            )

        except requests.Timeout as exc:
            raise GoCoreAPIError(
                f"Go Core request timed out: "
                f"{method} {url}"
            ) from exc

        except requests.ConnectionError as exc:
            raise GoCoreAPIError(
                f"Unable to connect to Go Core: "
                f"{method} {url}"
            ) from exc

        except requests.RequestException as exc:
            raise GoCoreAPIError(
                f"Go Core HTTP request failed: "
                f"{method} {url}: {exc}"
            ) from exc

        if not response.ok:
            try:
                error_payload = response.json()
            except ValueError:
                error_payload = response.text

            raise GoCoreAPIError(
                f"Go Core returned HTTP "
                f"{response.status_code} for "
                f"{method} {url}: "
                f"{error_payload}"
            )

        try:
            payload = response.json()
        except ValueError as exc:
            raise GoCoreAPIError(
                f"Go Core returned invalid JSON for "
                f"{method} {url}"
            ) from exc

        if not isinstance(payload, dict):
            raise GoCoreAPIError(
                f"Go Core returned unexpected JSON type "
                f"{type(payload).__name__} for "
                f"{method} {url}"
            )

        return payload

    # =========================================================
    # Go Core endpoint methods
    # =========================================================

    def health(self) -> dict[str, Any]:
        return self.get("/health")

    def stats(self) -> dict[str, Any]:
        return self.get("/stats")

    def snapshots(
        self,
        limit: int = 1000,
        root: str | None = None,
    ) -> dict[str, Any]:

        params: dict[str, Any] = {
            "limit": limit,
        }

        if root:
            params["root"] = root

        return self.get(
            "/snapshots",
            params=params,
        )

    def duplicates(
        self,
        full: bool = False,
        workers: int | None = None,
    ) -> dict[str, Any]:

        params: dict[str, Any] = {
            "full": str(full).lower(),
        }

        if workers is not None and workers > 0:
            params["workers"] = workers

        return self.get(
            "/files/duplicates",
            params=params,
        )

    def stale_files(
        self,
        days: int = 30,
        min_score: float = 0.05,
        limit: int = 100,
    ) -> dict[str, Any]:

        return self.get(
            "/files/stale",
            params={
                "days": days,
                "min_score": min_score,
                "limit": limit,
            },
        )

    def action_history(
        self,
        limit: int = 100,
    ) -> dict[str, Any]:

        return self.get(
            "/actions/history",
            params={
                "limit": limit,
            },
        )

    # =========================================================
    # Phase 6-capable write endpoints
    # =========================================================

    def execute_action(
        self,
        ids: list[int],
        mode: str = "trash",
    ) -> dict[str, Any]:

        return self.post(
            "/actions",
            payload={
                "ids": ids,
                "mode": mode,
            },
        )

    def restore_action(
        self,
        action_id: int,
    ) -> dict[str, Any]:

        return self.post(
            "/actions/restore",
            payload={
                "action_id": action_id,
            },
        )
