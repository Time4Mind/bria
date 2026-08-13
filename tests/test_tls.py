from __future__ import annotations

import pytest

from bria.transport.tls import server_ssl_context


def test_server_tls_requires_certificate_and_key(tmp_path) -> None:
    with pytest.raises(ValueError, match="both BRIA_TLS_CERT"):
        server_ssl_context(tmp_path / "cert.pem", None)


def test_server_tls_is_optional() -> None:
    assert server_ssl_context(None, None) is None
