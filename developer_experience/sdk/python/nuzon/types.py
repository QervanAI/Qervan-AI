# types.py - Enterprise Core Type Definitions
from datetime import datetime
from enum import Enum
from typing import Any, Dict, List, Literal, Optional, Union
from uuid import UUID
from pydantic import (
    BaseModel,
    Field,
    confloat,
    conint,
    conlist,
    constr,
    validator,
    root_validator
)

# region Enums
class AgentState(str, Enum):
    """Operational state machine for AI agents"""
    BOOTSTRAPPING = "BOOTSTRAPPING"
    ACTIVE = "ACTIVE"
    STANDBY = "STANDBY"
    MAINTENANCE = "MAINTENANCE"
    DECOMMISSIONED = "DECOMMISSIONED"

class ComplianceStandard(str, Enum):
    """Supported regulatory frameworks"""
    GDPR = "GDPR"
    HIPAA = "HIPAA"
    PCI_DSS = "PCI_DSS"
    SOC2 = "SOC2"
    ISO27001 = "ISO27001"

class MessageType(str, Enum):
    """Agent communication protocol types"""
    REQUEST = "REQUEST"
    RESPONSE = "RESPONSE"
    ERROR = "ERROR"
    HEARTBEAT = "HEARTBEAT"
    BROADCAST = "BROADCAST"

class EncryptionAlgorithm(str, Enum):
    """Supported cryptographic standards"""
    AES256_GCM = "AES256-GCM"
    CHACHA20_POLY1305 = "CHACHA20-POLY1305"
    KYBER768 = "KYBER768"
    ML_KEM_1024 = "ML-KEM-1024"
# endregion

# region Base Models
class SecurityContext(BaseModel):
    """Cryptographic operation parameters"""
    algorithm: EncryptionAlgorithm
    key_version: conint(ge=1, le=65535) = Field(
        default=1,
        description="Key rotation version identifier"
    )
    certificate_chain: Optional[List[str]] = Field(
        None,
        min_items=1,
        max_items=5,
        description="X.509 certificate chain for TLS"
    )

class AuditMetadata(BaseModel):
    """Compliance audit trail data"""
    timestamp: datetime = Field(
        default_factory=datetime.utcnow,
        description="Event occurrence time in UTC"
    )
    principal: constr(regex=r"^[a-zA-Z0-9_-]{1,255}$")
    source_ip: constr(regex=r"^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$")
    correlation_id: UUID

class ErrorDetail(BaseModel):
    """Standardized error reporting"""
    code: constr(regex=r"^[A-Z0-9_]{1,32}$")
    message: str = Field(..., min_length=1, max_length=2048)
    stack_trace: Optional[List[str]]
    remediation: Optional[str]
# endregion

# region Core Types
class AgentConfig(BaseModel):
    """Agent runtime configuration schema"""
    id: UUID
    name: constr(min_length=1, max_length=255)
    version: constr(regex=r"^\d+\.\d+\.\d+(-[a-zA-Z0-9]+)?$")
    compliance: List[ComplianceStandard] = Field(
        default=[ComplianceStandard.GDPR],
        min_items=1,
        max_items=5
    )
    security: SecurityContext
    performance: Dict[str, confloat(ge=0.0, le=1.0)] = Field(
        default={"cpu_threshold": 0.8},
        description="Resource utilization limits"
    )

    @validator("performance")
    def validate_performance_keys(cls, v):
        allowed = {"cpu_threshold", "memory_threshold", "network_latency"}
        if not v.keys() <= allowed:
            raise ValueError("Invalid performance metric")
        return v

class AgentMessage(BaseModel):
    """Inter-agent communication payload"""
    message_id: UUID
    type: MessageType
    payload: Dict[str, Any]
    context: Dict[str, Any] = Field(
        default_factory=dict,
        description="Session preservation data"
    )
    security: SecurityContext
    audit: AuditMetadata

    @root_validator
    def validate_payload_size(cls, values):
        payload = values.get("payload")
        if payload and len(str(payload)) > 102400:
            raise ValueError("Payload exceeds 100KB limit")
        return values

class HealthCheckResult(BaseModel):
    """System health monitoring data"""
    component: constr(regex=r"^[a-zA-Z0-9_-]{1,63}$")
    status: Literal["OK", "WARNING", "CRITICAL"]
    metrics: Dict[str, Union[float, int, str]]
    last_checked: datetime

class DeploymentSpec(BaseModel):
    """Cluster resource allocation template"""
    min_replicas: conint(ge=1, le=1000) = 3
    max_replicas: conint(ge=1, le=1000) = 100
    scaling: conlist(
        item_type=conint(ge=1, le=100),
        min_items=2,
        max_items=2
    ) = [50, 80]
    availability_zones: List[conint(ge=1, le=3)] = [1, 2, 3]
# endregion

# region Response Types  
class APIResponse(BaseModel):
    """Standard API response envelope"""
    data: Optional[Union[Dict, List]]
    error: Optional[ErrorDetail]
    meta: Dict[str, Any] = Field(
        default_factory=dict,
        description="Pagination/rate limit info"
    )

class AuditReport(BaseModel):
    """Compliance audit documentation"""
    period_start: datetime
    period_end: datetime
    events: List[AuditMetadata]
    violations: conint(ge=0) = 0
    certified: bool

class ClusterTelemetry(BaseModel):
    """Real-time monitoring data"""
    timestamp: datetime
    nodes: conint(ge=1)
    active_agents: conint(ge=0)
    resource_utilization: Dict[str, confloat(ge=0.0)]
# endregion

# region Validation Utilities
def validate_iso8601_datetime(value: str) -> datetime:
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as e:
        raise ValueError(f"Invalid ISO 8601 datetime: {e}")

def validate_encryption_context(ctx: Dict[str, str]) -> None:
    if not all(len(k) <= 64 and len(v) <= 512 for k, v in ctx.items()):
        raise ValueError("Encryption context exceeds size limits")
# endregion

__all__ = [
    "AgentState",
    "ComplianceStandard",
    "MessageType",
    "EncryptionAlgorithm",
    "SecurityContext",
    "AuditMetadata",
    "ErrorDetail",
    "AgentConfig",
    "AgentMessage",
    "HealthCheckResult",
    "DeploymentSpec",
    "APIResponse",
    "AuditReport",
    "ClusterTelemetry",
    "validate_iso8601_datetime",
    "validate_encryption_context"
]
