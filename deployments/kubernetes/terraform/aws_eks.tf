# aws_eks.tf - Enterprise Kubernetes Cluster Infrastructure
terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.16.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.23.0"
    }
  }
}

# Base Networking
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.1.0"

  name = "${local.cluster_name}-vpc"
  cidr = "10.42.0.0/16"

  azs             = ["${local.region}a", "${local.region}b", "${local.region}c"]
  private_subnets = ["10.42.1.0/24", "10.42.2.0/24", "10.42.3.0/24"]
  public_subnets  = ["10.42.101.0/24", "10.42.102.0/24", "10.42.103.0/24"]

  enable_nat_gateway   = true
  single_nat_gateway   = false
  enable_dns_hostnames = true

  public_subnet_tags = {
    "kubernetes.io/role/elb"              = "1"
    "nuzon.ai/network-tier"               = "public"
  }

  private_subnet_tags = {
    "kubernetes.io/role/internal-elb"     = "1" 
    "nuzon.ai/network-tier"               = "private"
  }

  tags = local.tags
}

# Enterprise EKS Cluster
module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "19.16.0"

  cluster_name                    = local.cluster_name
  cluster_version                 = "1.28"
  cluster_endpoint_public_access  = true
  cluster_endpoint_private_access = true

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets

  cluster_encryption_config = {
    provider_key_arn = aws_kms_key.eks.arn
    resources        = ["secrets"]
  }

  # Production Node Groups
  eks_managed_node_groups = {
    core-ondemand = {
      name           = "core-ondemand"
      instance_types = ["m6i.xlarge", "m6a.xlarge"]
      capacity_type  = "ON_DEMAND"
      min_size       = 3
      max_size       = 12
      desired_size   = 6

      labels = {
        WorkloadType = "stateful"
      }

      tags = {
        "k8s.io/cluster-autoscaler/enabled"               = "true"
        "k8s.io/cluster-autoscaler/${local.cluster_name}" = "owned"
      }
    }

    spot-workers = {
      name           = "spot-workers"
      instance_types = ["m6i.xlarge", "m5.xlarge", "m5n.xlarge"]
      capacity_type  = "SPOT"
      min_size       = 6
      max_size       = 100
      desired_size   = 12

      labels = {
        WorkloadType = "stateless"
      }

      taints = [{
        key    = "spot"
        value  = "true"
        effect = "NO_SCHEDULE"
      }]

      tags = {
        "k8s.io/cluster-autoscaler/enabled"               = "true"
        "k8s.io/cluster-autoscaler/${local.cluster_name}" = "owned"
      }
    }
  }

  node_security_group_additional_rules = {
    ingress_cluster_apis = {
      description                   = "Cluster API access"
      protocol                      = "tcp"
      from_port                     = 443
      to_port                       = 443
      type                          = "ingress"
      source_cluster_security_group = true
    }
  }

  tags = local.tags
}

# Enterprise Security Controls
resource "aws_kms_key" "eks" {
  description             = "EKS Secret Encryption Key"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.kms.json
  tags                    = local.tags
}

resource "aws_security_group" "worker_network" {
  name        = "${local.cluster_name}-worker-sg"
  description = "EKS Worker Node Security Group"
  vpc_id      = module.vpc.vpc_id

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(local.tags, {
    Name = "${local.cluster_name}-worker-sg"
  })
}

# IAM Roles for Service Accounts (IRSA)
module "iam_roles" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role-with-oidc"
  version = "5.28.0"

  for_each = local.irsa_applications

  create_role      = true
  role_name        = "${local.cluster_name}-${each.key}-role"
  role_description = "IRSA Role for ${each.key}"
  provider_url     = replace(module.eks.cluster_oidc_issuer_url, "https://", "")
  role_policy_arns = each.value.policy_arns

  tags = local.tags
}

# Monitoring & Logging
resource "aws_cloudwatch_log_group" "eks" {
  name              = "/aws/eks/${local.cluster_name}/cluster"
  retention_in_days = 30
  kms_key_id        = aws_kms_key.eks.arn
}

resource "aws_prometheus_workspace" "monitoring" {
  alias = "${local.cluster_name}-prometheus"
  logging_configuration {
    log_group_arn = "${aws_cloudwatch_log_group.eks.arn}:*"
  }
}

# Outputs
output "kubeconfig" {
  value = module.eks.kubeconfig
  sensitive = true
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}

locals {
  cluster_name = "nuzon-ai-prod"
  region       = "us-west-2"
  tags = {
    Environment  = "Production"
    ManagedBy    = "Terraform"
    Compliance   = "SOC2"
    DataClass    = "Restricted"
  }
  irsa_applications = {
    vpc-cni = {
      policy_arns = [aws_iam_policy.vpc_cni.arn]
    }
    aws-load-balancer = {
      policy_arns = [aws_iam_policy.lb_controller.arn]
    }
  }
}

data "aws_iam_policy_document" "kms" {
  statement {
    sid    = "AllowKMSManagement"
    effect = "Allow"
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }
    actions   = ["kms:*"]
    resources = ["*"]
  }
}
