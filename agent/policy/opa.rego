# opa.rego - Enterprise Policy Enforcement Core
package policy.enterprise

import future.keywords

########################################
# Main Authorization Entry Point
########################################

default allow := false
default allowed_operations := []
default filtered_data := {}

allow {
    # RBAC Validation
    role_valid
    
    # ABAC Validation
    environment_match
    
    # Temporal Constraints
    time_window_valid
    
    # Request Integrity
    request_signature_valid
    
    # Defense in Depth
    threat_level_acceptable
}

allowed_operations[operation] {
    some operation
    role_operations[input.user.role][_] == operation
    temporal_restrictions[operation]
}

filtered_data = object.filter(data, filter_predicate) {
    data := input.resource.data
}

########################################
# Attribute-Based Access Control (ABAC)
########################################

environment_match {
    input.resource.environment == input.user.clearance.env
}

threat_level_acceptable {
    input.threat.level < 5
}

request_signature_valid {
    crypto.x509.parse_and_verify(input.request.signature)
    crypto.hmac.equal(input.request.signature, generate_hmac(input))
}

########################################
# Role-Based Access Control (RBAC)
########################################

role_valid {
    data.roles[input.user.role]
}

role_operations := {
    "admin": ["read", "write", "delete", "execute"],
    "operator": ["read", "execute"],
    "auditor": ["read"]
}

########################################
# Temporal & Contextual Constraints
########################################

time_window_valid {
    time.now_ns() >= input.user.schedule.start
    time.now_ns() <= input.user.schedule.end
}

temporal_restrictions[op] {
    not is_restricted_operation(op)
}

is_restricted_operation(op) {
    time.clock(time.now())[1] >= 22  # After 10PM
    sensitive_operations[_] == op
}

sensitive_operations := ["delete", "execute"]

########################################
# Data Filtering & Transformation
########################################

filter_predicate(key, value) = result {
    data_classification[key] == classification
    classification_clearance[classification] <= input.user.clearance.level
    result := true
} else = false {
    true
}

data_classification := {
    "ssn": "PII",
    "diagnosis": "PHI",
    "transaction": "PCI"
}

classification_clearance := {
    "PII": 3,
    "PHI": 4,
    "PCI": 3
}

########################################
# Audit & Compliance Enforcement
########################################

violation[msg] {
    not allow
    msg := sprintf("Access denied: %s", [concat(", ", reasons)])
}

reasons contains reason {
    not role_valid
    reason := "Invalid role assignment"
} else contains reason {
    not environment_match
    reason := "Environment mismatch"
} else contains reason {
    not time_window_valid
    reason := "Outside permitted time window"
}

########################################
# Enterprise Security Extensions
########################################

certificate_pinned {
    crypto.sha256(input.certificate) == data.trusted_certs[input.certificate.issuer]
}

geo_compliance {
    not restricted_countries[input.user.location.country]
}

restricted_countries := {"CU", "IR", "KP", "SY"}

########################################
# System Resource Governance
########################################

max_concurrent_sessions := 25

session_violation[msg] {
    count(data.sessions[input.user.id]) >= max_concurrent_sessions
    msg := "Maximum session limit reached"
}
