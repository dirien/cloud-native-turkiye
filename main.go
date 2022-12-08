package main

import (
	"fmt"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/eks"
	"github.com/pulumi/pulumi-aws/sdk/v5/go/aws/iam"
	"github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes"
	"github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func BoolPtr(b bool) *bool {
	return &b
}

func toPulumiStringArray(a []string) pulumi.StringArrayInput {
	var res []pulumi.StringInput
	for _, s := range a {
		res = append(res, pulumi.String(s))
	}
	return pulumi.StringArray(res)
}

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{
			Default: BoolPtr(true),
		})
		if err != nil {
			return err
		}

		subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{
			Filters: []ec2.GetSubnetsFilter{
				{
					Name: "vpc-id",
					Values: []string{
						vpc.Id,
					},
				},
			},
		})

		eksRole, err := iam.NewRole(ctx, "eks-iam-role", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
			"Version": "2012-10-17",
			"Statement": [
				{
					"Effect": "Allow",
					"Principal": {
						"Service": "eks.amazonaws.com"
					},
					"Action": "sts:AssumeRole"	
				}
			]
		}`),
		})
		if err != nil {
			return err
		}

		nodeRole, err := iam.NewRole(ctx, "node-iam-role", &iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
			"Version": "2012-10-17",
			"Statement": [
				{
					"Effect": "Allow",
					"Principal": {
						"Service": "ec2.amazonaws.com"
					},
					"Action": "sts:AssumeRole"
				}
			]
		}`),
		})
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, "%s-node-iam-role-attachment", &iam.RolePolicyAttachmentArgs{
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"),
			Role:      nodeRole.Name,
		})
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, "node-iam-role-attachment2", &iam.RolePolicyAttachmentArgs{
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"),
			Role:      nodeRole.Name,
		})
		if err != nil {
			return err
		}
		_, err = iam.NewRolePolicyAttachment(ctx, "node-iam-role-attachment3", &iam.RolePolicyAttachmentArgs{
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"),
			Role:      nodeRole.Name,
		})
		if err != nil {
			return err
		}

		securityGroup, err := ec2.NewSecurityGroup(ctx, "eks-sg", &ec2.SecurityGroupArgs{
			Description: pulumi.String("EKS Security Group"),
			Ingress: ec2.SecurityGroupIngressArray{
				ec2.SecurityGroupIngressArgs{
					Description: pulumi.String("Allow Minecraft from VPC"),
					FromPort:    pulumi.Int(25565),
					Protocol:    pulumi.String("tcp"),
					ToPort:      pulumi.Int(25565),
					CidrBlocks: pulumi.StringArray{
						pulumi.String("0.0.0.0/0"),
					},
				},
			},
			Egress: ec2.SecurityGroupEgressArray{
				ec2.SecurityGroupEgressArgs{
					Description: pulumi.String("Allow all outbound traffic"),
					FromPort:    pulumi.Int(0),
					Protocol:    pulumi.String("-1"),
					ToPort:      pulumi.Int(0),
					CidrBlocks: pulumi.StringArray{
						pulumi.String("0.0.0.0/0"),
					},
				},
			},
			VpcId: pulumi.String(vpc.Id),
		})
		if err != nil {
			return err
		}

		eksCluster, err := eks.NewCluster(ctx, "eks-cluster", &eks.ClusterArgs{
			RoleArn: eksRole.Arn,
			VpcConfig: &eks.ClusterVpcConfigArgs{
				SubnetIds: toPulumiStringArray(subnets.Ids),
				PublicAccessCidrs: pulumi.StringArray{
					pulumi.String("0.0.0.0/0"),
				},
				SecurityGroupIds: pulumi.StringArray{
					securityGroup.ID(),
				},
			},
			Tags: pulumi.StringMap{
				"Name": pulumi.String("my-mincraft-eks-cluster"),
			},
		})
		if err != nil {
			return err
		}

		nodeGroup, err := eks.NewNodeGroup(ctx, "eks-node-group", &eks.NodeGroupArgs{
			ClusterName:   eksCluster.Name,
			NodeRoleArn:   nodeRole.Arn,
			SubnetIds:     toPulumiStringArray(subnets.Ids),
			NodeGroupName: pulumi.String("eks-node-group"),
			ScalingConfig: &eks.NodeGroupScalingConfigArgs{
				DesiredSize: pulumi.Int(2),
				MaxSize:     pulumi.Int(2),
				MinSize:     pulumi.Int(1),
			},
		})
		if err != nil {
			return err
		}

		kubeconfig := pulumi.All(eksCluster.Name, eksCluster.Endpoint, eksCluster.CertificateAuthority).ApplyT(func(args []interface{}) (string, error) {
			name := args[0].(string)
			endpoint := args[1].(string)
			ca := args[2].(eks.ClusterCertificateAuthority)
			kubeconfig := fmt.Sprintf(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: aws
  name: aws
current-context: aws
kind: Config
users:
- name: aws
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws-iam-authenticator
      args:
        - "token"
        - "-i"
        - "%s"
`, *ca.Data, endpoint, name)
			return kubeconfig, nil

		}).(pulumi.StringOutput)

		k8sProvider, err := kubernetes.NewProvider(ctx, "k8sprovider", &kubernetes.ProviderArgs{
			Kubeconfig: kubeconfig,
		}, pulumi.DependsOn([]pulumi.Resource{nodeGroup}))

		_, err = helm.NewRelease(ctx, "minecraft", &helm.ReleaseArgs{
			Chart:   pulumi.String("minecraft"),
			Version: pulumi.String("4.4.0"),
			RepositoryOpts: &helm.RepositoryOptsArgs{
				Repo: pulumi.String("https://itzg.github.io/minecraft-server-charts"),
			},
			CreateNamespace: pulumi.Bool(true),
			Namespace:       pulumi.String("minecraft"),
			Values: pulumi.Map{
				"minecraftServer": pulumi.Map{
					"eula":        pulumi.Bool(true),
					"motd":        pulumi.String("Cloud Native Türkiye - Minecraft Server"),
					"serviceType": pulumi.String("LoadBalancer"),
				},
			},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return err
		}

		return nil
	})
}
