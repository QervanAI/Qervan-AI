# client.py - Enterprise AI Agent Python SDK 
import json
import logging
import os
from datetime import datetime
from typing import Any, AsyncIterator, Dict, List, Optional, Union
from pydantic import BaseModel, Field, ValidationError, validator
import httpx
from tenacity import (
    retry,
    stop_after_attempt,
    wait_exponential,
    retry_if_exception_type,
    before_log
)

logger = logging.getLogger(__name__)

class NuzonConfig(BaseModel):
    """Enterprise-grade client configuration"""
    base_url: str = Field(
        default=os.getenv("cirium_API_URL", "https://api.cirium.ai/v3"),
        description="Base API endpoint URL"
    )
    api_key: str = Field(
        ...,
        min_length=64,
        max_length=256,
        description="HMAC-encrypted API key"
    )
    timeout: float = Field(
        default=30.0,
        ge=1.0,
        le=120.0,
        description="Global request timeout in seconds"
    )
    max_retries: int = Field(
        default=5,
        ge=0,
        le=10,
        description="Max automatic retry attempts"
    )
    enable_telemetry: bool = Field(
        default=True,
        description="Enable performance metrics collection"
    )
    circuit_breaker_threshold: int = Field(
        default=5,
        description="Consecutive failures before circuit opens"
    )

    class Config:
        env_prefix = "NUZON_"
        extra = "forbid"

class AgentRequest(BaseModel):
    """Validated agent interaction payload"""
    conversation_id: str = Field(
        ...,
        regex=r"^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-4[a-fA-F0-9]{3}-[89aABb][a-fA-F0-9]{3}-[a-fA-F0-9]{12}$",
        description="UUIDv4 conversation identifier"
    )
    input_data: Dict[str, Any] = Field(
        ...,
        min_items=1,
        max_items=1000,
        description="Structured input payload"
    )
    context: Optional[Dict[str, Any]] = Field(
        None,
        description="Session context preservation"
    )
    safety_filters: List[str] = Field(
        ["gdpr", "pci_dss"],
        description="Active compliance filters"
    )

    @validator("input_data")
    def validate_input_size(cls, v):
        if len(json.dumps(v)) > 102400:
            raise ValueError("Input payload exceeds 100KB limit")
        return v

class AgentResponse(BaseModel):
    """Validated agent response schema"""
    success: bool
    result: Dict[str, Any]
    metrics: Dict[str, float]
    compliance_checks: Dict[str, bool]
    request_id: str
    timestamp: datetime

class NuzonError(Exception):
    """Base exception for SDK errors"""
    def __init__(self, message: str, code: int, context: dict):
        super().__init__(message)
        self.code = code
        self.context = context

class NuzonClient:
    """Enterprise-grade client implementation"""
    
    def __init__(self, config: Union[NuzonConfig, Dict]):
        self.config = config if isinstance(config, NuzonConfig) else NuzonConfig(**config)
        self._client = self._init_sync_client()
        self._async_client = self._init_async_client()
        self._circuit_open = False
        self._failure_count = 0

    def _init_sync_client(self):
        return httpx.Client(
            base_url=self.config.base_url,
            timeout=self.config.timeout,
            headers=self._default_headers(),
            limits=httpx.Limits(
                max_connections=100,
                max_keepalive_connections=20
            ),
            event_hooks=self._get_event_hooks()
        )

    def _init_async_client(self):
        return httpx.AsyncClient(
            base_url=self.config.base_url,
            timeout=self.config.timeout,
            headers=self._default_headers(),
            limits=httpx.Limits(
                max_connections=100,
                max_keepalive_connections=20
            ),
            event_hooks=self._get_event_hooks()
        )

    def _default_headers(self):
        return {
            "Authorization": f"Bearer {self.config.api_key}",
            "X-Nuzon-SDK-Version": "3.4.0",
            "Content-Type": "application/json",
            "Accept": "application/json; version=3"
        }

    def _get_event_hooks(self):
        return {
            "request": [self._sign_request],
            "response": [self._validate_response]
        }

    def _sign_request(self, request):
        # HMAC-based request signing
        timestamp = str(int(datetime.now().timestamp()))
        payload = f"{request.method}{request.url}{timestamp}".encode()
        signature = hmac.new(
            self.config.api_key.encode(),
            payload,
            hashlib.sha256
        ).hexdigest()
        request.headers.update({
            "X-Nuzon-Timestamp": timestamp,
            "X-Nuzon-Signature": signature
        })
        return request

    def _validate_response(self, response):
        if response.status_code >= 500:
            self._failure_count += 1
            if self._failure_count >= self.config.circuit_breaker_threshold:
                self._circuit_open = True
        elif response.is_success:
            self._failure_count = 0
            self._circuit_open = False
        return response

    @retry(
        stop=stop_after_attempt(3),
        wait=wait_exponential(multiplier=1, max=10),
        retry=retry_if_exception_type((httpx.NetworkError, httpx.TimeoutException)),
        before=before_log(logger, logging.DEBUG)
    )
    def execute(self, request: AgentRequest) -> AgentResponse:
        """Synchronous execution with retry logic"""
        if self._circuit_open:
            raise NuzonError("Circuit breaker active", 503, {})
        
        try:
            response = self._client.post(
                "/agents/execute",
                json=request.dict(exclude_none=True)
            )
            response.raise_for_status()
            return AgentResponse(**response.json())
        except httpx.HTTPStatusError as e:
            self._handle_http_error(e)
        except ValidationError as e:
            self._handle_validation_error(e)

    async def execute_async(self, request: AgentRequest) -> AgentResponse:
        """Asynchronous execution with retry logic"""
        if self._circuit_open:
            raise NuzonError("Circuit breaker active", 503, {})
        
        async with self._async_client as client:
            try:
                response = await client.post(
                    "/agents/execute",
                    json=request.dict(exclude_none=True)
                )
                response.raise_for_status()
                return AgentResponse(**response.json())
            except httpx.HTTPStatusError as e:
                self._handle_http_error(e)
            except ValidationError as e:
                self._handle_validation_error(e)

    def stream(self, request: AgentRequest) -> AsyncIterator[AgentResponse]:
        """Real-time streaming execution"""
        with self._client.stream(
            "POST",
            "/agents/stream",
            json=request.dict(exclude_none=True)
        ) as response:
            for chunk in response.iter_lines():
                yield AgentResponse(**json.loads(chunk))

    def _handle_http_error(self, error: httpx.HTTPStatusError):
        error_body = error.response.json()
        raise NuzonError(
            message=error_body.get("message", "Unknown error"),
            code=error.response.status_code,
            context=error_body.get("details", {})
        )

    def _handle_validation_error(self, error: ValidationError):
        raise NuzonError(
            message="Validation failed",
            code=422,
            context={"errors": error.errors()}
        )

    def close(self):
        self._client.close()
        self._async_client.close()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.close()

# Example usage
if __name__ == "__main__":
    config = NuzonConfig(
        api_key=os.environ["NUZON_API_KEY"],
        timeout=20.0
    )
    
    with NuzonClient(config) as client:
        request = AgentRequest(
            conversation_id="123e4567-e89b-12d3-a456-426614174000",
            input_data={"query": "Enterprise AI analysis request"}
        )
        
        try:
            response = client.execute(request)
            print(f"Response received: {response.result}")
        except NuzonError as e:
            print(f"Error {e.code}: {e}")
