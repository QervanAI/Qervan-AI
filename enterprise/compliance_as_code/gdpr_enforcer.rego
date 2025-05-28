# gdpr_enforcer.rego - Enterprise Data Protection Automation

package gdpr.enforcement
 
import future.keywords.in

########################################
# Data Models
########################################

# GDPR Subject Request Object
subject_request := {
    "type": "access",       # access/erasure/portability/rectification
    "id": "12345",          # Request tracking ID
    "user_id": "usr-67890", # Subject identifier
    "data_categories": ["biometric", "contact"],
    "jurisdiction": "EEA"
}

# Data Inventory Representation
data_inventory := [
    {
        "id": "di-001",
        "type": "contact",
        "owner": "usr-67890",
        "processed_by": ["app-1", "analytics-3"],
        "legal_basis": "consent",
        "retention_days": 90,
        "encrypted": true
    },
    {
        "id": "di-002",
        "type": "biometric",
        "owner": "usr-67890",
        "processed_by": ["auth-service"],
        "legal_basis": "contract",
        "retention_days": 365,
        "encrypted": true
    }
]

########################################
# Core Policy Engine
########################################

# Article 15: Right of Access
default allow_access = false
allow_access {
    is_verified_identity(input.user_id)
    valid_legal_basis_for_access(input.data_categories)
    not has_legal_restriction(input.user_id)
}

# Article 17: Right to Erasure 
default allow_erasure = false
allow_erasure {
    is_verified_identity(input.user_id)
    not has_legal_hold(input.data_categories)
    not required_for_public_interest(input.data_categories)
}

# Article 6: Lawfulness of Processing
default lawful_processing = false
lawful_processing {
    valid_legal_basis(input.legal_basis)
    data_minimization_compliance(input.data_categories)
    purpose_limitation_check(input.processing_purpose)
}

# Article 33: Breach Notification
breach_notification_required {
    input.breach.severity >= 3
    input.breach.affected_subjects > 100
    input.breach.data_types_affected in ["sensitive", "special_category"]
}

########################################
# Compliance Checks
########################################

valid_legal_basis(basis) {
    basis in {"consent", "contract", "legal_obligation", "vital_interest", "public_task", "legitimate_interest"}
}

data_minimization_compliance(categories) {
    count(categories) <= 5
    not "unnecessary" in categories
}

purpose_limitation_check(purpose) {
    original_purposes := {p | p := data_inventory[_].processing_purposes}
    purpose in original_purposes
}

########################################
# Security Controls
########################################

default encryption_required = false
encryption_required {
    input.data_classification in {"confidential", "restricted"}
    input.storage_location != "on-premise"
}

data_retention_compliance {
    input.retention_days <= max_retention_period(input.data_type)
    input.retention_days >= min_retention_period(input.data_type)
}

########################################
# Enterprise Features
########################################

# Multi-tenant Isolation
allow_cross_tenant_access {
    input.requesting_tenant == input.data_owner_tenant
    input.sharing_agreement_valid == true
}

# Audit Trail Generation
audit_event := {"timestamp": time.now_ns(), "decision": decision} {
    decision := {"allow": allow_access, "deny": not allow_access}
}

# Automated Redaction Rules
redaction_required(field) {
    field.sensitivity_class >= 3
    field.pii_type in ["SSN", "health_record"]
    not field.consent_status == "explicit"
}

########################################
# Helper Functions
########################################

is_verified_identity(user_id) {
    startswith(user_id, "usr-")
    count(user_id) == 12
    user_verification_status[user_id] == "confirmed"
}

has_legal_restriction(user_id) {
    legal_holds[_] == user_id
}

max_retention_period(data_type) = period {
    data_type == "financial"
    period := 365 * 5
} else := 365 {
    data_type == "biometric"
}

min_retention_period(data_type) = period {
    data_type == "transaction"
    period := 30
} else := 0
