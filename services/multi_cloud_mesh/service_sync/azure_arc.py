# azure_arc.py - Enterprise Azure Arc Controller
import os
import json
from datetime import datetime
from typing import Dict, List, Optional

from azure.identity import ClientSecretCredential
from azure.mgmt.resourcegraph import ResourceGraphClient
from azure.mgmt.resourcegraph.models import QueryRequest
from azure.mgmt.kubernetesconfiguration import SourceControlConfigurationClient
from azure.mgmt.kubernetesconfiguration.models import (
    SourceControlConfiguration,
    HelmOperatorProperties,
    ComplianceStatus,
    ConfigurationProtectedSettings
)

class AzureArcController:
    def __init__(self, tenant_id: str, client_id: str, client_secret: str):
        self.credential = ClientSecretCredential(
            tenant_id=tenant_id,
            client_id=client_id,
            client_secret=client_secret
        )
        self.resource_graph = ResourceGraphClient(self.credential)
        self.source_control_client = SourceControlConfigurationClient(self.credential)

    def connect_cluster(self, cluster_resource_id: str, tags: Dict[str, str]) -> Dict:
        """Register Kubernetes cluster with Azure Arc"""
        query = f"""
        Resources
        | where type =~ 'Microsoft.Kubernetes/connectedClusters'
        | where id == '{cluster_resource_id}'
        | project name, location, properties
        """
        
        response = self.resource_graph.resources(QueryRequest(
            subscriptions=[os.getenv("AZURE_SUBSCRIPTION_ID")],
            query=query,
            options={"resultFormat": "table"}
        ))
        
        if not response.data:
            raise ValueError(f"Cluster {cluster_resource_id} not found in Azure Arc")
            
        cluster_data = json.loads(response.data[0]["properties"])
        self._apply_tags(cluster_resource_id, tags)
        return cluster_data

    def deploy_extension(self, 
                        cluster_resource_id: str, 
                        extension_name: str,
                        config_settings: Dict[str, str],
                        helm_chart: Dict) -> SourceControlConfiguration:
        """Deploy Azure Arc extensions with enterprise security"""
        protected_settings = ConfigurationProtectedSettings(
            **{k: v for k, v in config_settings.items() if k.startswith('secure.')}
        )
        
        configuration = SourceControlConfiguration(
            repository_url=helm_chart["repo"],
            operator_namespace=extension_name,
            operator_instance_name=f"{extension_name}-instance",
            operator_type="helm",
            operator_params="--set global.azure.arc=true",
            configuration_protected_settings=protected_settings,
            enable_helm_operator=True,
            helm_operator_properties=HelmOperatorProperties(
                chart_values=json.dumps(helm_chart.get("values", {})),
                chart_version=helm_chart["version"]
            ),
            compliance_status=ComplianceStatus(
                compliance_state="Pending",
                last_config_applied=datetime.utcnow().isoformat()
            )
        )
        
        return self.source_control_client.extensions.begin_create_or_update(
            resource_group_name=self._parse_rg(cluster_resource_id),
            cluster_rp="Microsoft.Kubernetes",
            cluster_resource_name="connectedClusters",
            cluster_name=self._parse_name(cluster_resource_id),
            extension_name=extension_name,
            configuration=configuration
        ).result()

    def enforce_policy(self, 
                     cluster_resource_id: str, 
                     policy_definitions: List[Dict]) -> Dict:
        """Apply Azure Policy to hybrid clusters"""
        policy_client = PolicyClient(self.credential)
        assignments = []
        
        for policy in policy_definitions:
            assignment = PolicyAssignment(
                display_name=policy["name"],
                policy_definition_id=policy["id"],
                scope=cluster_resource_id,
                parameters=policy.get("parameters"),
                enforcement_mode="Default"
            )
            result = policy_client.policy_assignments.create(
                scope=cluster_resource_id,
                policy_assignment_name=f"nuzon-{policy['name']}",
                parameters=assignment
            )
            assignments.append(result)
            
        return {
            "cluster": cluster_resource_id,
            "policies_applied": [a.display_name for a in assignments]
        }

    def _apply_tags(self, resource_id: str, tags: Dict[str, str]) -> None:
        """Apply resource tags with Azure Policy compliance"""
        from azure.mgmt.resource import ResourceManagementClient
        resource_client = ResourceManagementClient(self.credential, os.getenv("AZURE_SUBSCRIPTION_ID"))
        
        parts = resource_id.split('/')
        resource_group = parts[4]
        resource_name = parts[-1]
        
        resource = resource_client.resources.get_by_id(
            resource_id=resource_id,
            api_version="2021-04-01"
        )
        
        updated_resource = resource_client.resources.begin_create_or_update_by_id(
            resource_id=resource_id,
            api_version="2021-04-01",
            parameters={
                "location": resource.location,
                "tags": tags,
                "properties": resource.properties
            }
        ).result()
        
    def _parse_rg(self, resource_id: str) -> str:
        return resource_id.split('/')[4]
        
    def _parse_name(self, resource_id: str) -> str:
        return resource_id.split('/')[-1]

# Enterprise Deployment Example
if __name__ == "__main__":
    arc = AzureArcController(
        tenant_id=os.getenv("AZURE_TENANT_ID"),
        client_id=os.getenv("AZURE_CLIENT_ID"),
        client_secret=os.getenv("AZURE_CLIENT_SECRET")
    )
    
    # Connect production cluster
    cluster_data = arc.connect_cluster(
        "/subscriptions/{sub}/resourceGroups/{rg}/providers/Microsoft.Kubernetes/connectedClusters/nuzon-prod",
        tags={"environment": "prod", "compliance-tier": "pci"}
    )
    
    # Deploy security extensions
    arc.deploy_extension(
        cluster_resource_id=cluster_data["id"],
        extension_name="nuzon-security",
        config_settings={
            "secure.api_key": os.getenv("NUZON_API_KEY"),
            "audit.level": "verbose"
        },
        helm_chart={
            "repo": "https://nuzon-helm.azurecr.io/security",
            "version": "2.4.0",
            "values": {
                "quantumTLS": {
                    "enabled": True,
                    "kmsProvider": "azureKeyVault"
                },
                "autoRemediation": {
                    "enabled": True
                }
            }
        }
    )
    
    # Enforce enterprise policies
    arc.enforce_policy(
        cluster_data["id"],
        policy_definitions=[{
            "name": "nuzon-quantum-crypto",
            "id": "/providers/Microsoft.Authorization/policyDefinitions/quantum-crypto-2023",
            "parameters": {
                "minKeySize": {"value": 256},
                "allowedAlgorithms": {"value": ["KYBER", "NTRU"]}
            }
        }]
    )
