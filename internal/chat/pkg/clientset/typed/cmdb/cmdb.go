// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package cmdb provides a typed client for the CMDB resource-query API.
package cmdb

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // MD5 is required by the CMDB signing spec, not used for security
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"resty.dev/v3"

	baseclientset "github.com/onexstack/onexai/internal/pkg/clientset"
)

// ViewID represents different CMDB tree view identifiers
type ViewID int

const (
	// ViewDeptTree is the service tree's department view
	ViewDeptTree ViewID = 2
	// ViewBizTree is the service tree's business view (deprecated)
	ViewBizTree ViewID = 1
	// ViewEC2 represents the EC2 resources view
	ViewEC2 ViewID = 10001
	// ViewEKS represents the EKS resources view
	ViewEKS ViewID = 10002
	// ViewDatabase represents the database resources view
	ViewDatabase ViewID = 10003
)

// ResourceType represents different AWS resource types in CMDB
type ResourceType string

const (
	ResourceTypeVPC           ResourceType = "aws_vpc"
	ResourceTypeSubnet        ResourceType = "aws_subnet"
	ResourceTypeSecurityGroup ResourceType = "aws_security_group"
	ResourceTypeAWSRole       ResourceType = "aws_role"
)

// Interface defines the operations for CMDB client.
type Interface interface {
	// Search methods
	SearchPodNode(ctx context.Context, podName, clusterName, namespace string) (string, error)
	SearchEC2Instance(ctx context.Context, searchTerm string) ([]TreeNode, error)
	SearchBusinessService(ctx context.Context, serviceName, env string) (*BusinessServiceInfo, error)

	// Resource query methods
	GetNodeSummary(ctx context.Context, viewID ViewID, nodeID string) (*ServiceSummaryResponse, error)
	GetNetworkResources(ctx context.Context, viewID ViewID, nodeID string) (*NetworkResources, error)

	// Service methods
	GetServiceAWSRoles(ctx context.Context, serviceID, env string) ([]*AWSRoleInfo, error)

	// EC2-specific methods
	GetEC2InstanceDetails(ctx context.Context, nodeID string) (map[string]map[string]string, error)
	GetEC2AssociatedServices(ctx context.Context, nodeID string) ([]*BusinessServiceInfo, error)

	// Generic model query (agent-facing, returns raw records)
	SearchModel(ctx context.Context, modelID string, req SearchModelRequest) (*SearchModelResponse, error)

	// Schema introspection (agent-facing, returns raw column/key definitions)
	GetModelSchema(ctx context.Context, modelID string) (*ModelSchemaOutput, error)

	// GetServiceByIP queries the service and resource associated with an IP address.
	GetServiceByIP(ctx context.Context, ip string) (*ServiceByIPResponse, error)

	// GetServiceNetworkInfo queries the VPC, subnets, security groups, EC2 instances,
	// Pods and IAM roles associated with a service in a given environment.
	GetServiceNetworkInfo(ctx context.Context, serviceName, envCodeAlias string) (*ServiceNetworkInfo, error)

	// GetServiceLocation resolves the AWS account, Kubernetes cluster(s) and
	// namespace(s) that a service runs in for a given environment.
	GetServiceLocation(ctx context.Context, serviceName, envCodeAlias string) (*ServiceLocation, error)

	// GetServiceClusterId searches the cmdb service tree by serviceName and
	// returns the serviceclusterid via envAlias.
	GetServiceClusterId(ctx context.Context, serviceName, envAlias string) (string, error)
	GetEksClusterAndNamespace(ctx context.Context, serviceClusterId string) (string, string, string, error)

	// CheckServicePermission queries whether userID has the given permission
	// action on the named business service. Not yet backed by a real endpoint.
	CheckServicePermission(ctx context.Context, userID, serviceName string, action PermissionAction) (bool, error)
}

// PermissionAction identifies the kind of access being checked.
type PermissionAction string

const (
	PermissionActionRead  PermissionAction = "read"
	PermissionActionWrite PermissionAction = "write"
)

// ErrPermissionCheckNotImplemented is returned by Client.CheckServicePermission
// until a real CMDB endpoint contract is confirmed.
var ErrPermissionCheckNotImplemented = fmt.Errorf("cmdb: CheckServicePermission is not backed by a real endpoint yet")

// Client implements Interface using resty with per-request HMAC-SHA256 signing.
type Client struct {
	client    *resty.Client // base: /cmdbapi
	endpoint  string
	apiKey    string
	apiSecret string
}

var _ Interface = (*Client)(nil)
var _ baseclientset.Interface = (*Client)(nil)

// C returns the underlying resty client.
func (c *Client) C() *resty.Client { return c.client }

// R builds a request bound to ctx.
func (c *Client) R(ctx context.Context) *resty.Request { return c.client.R().SetContext(ctx) }

// Healthz checks the CMDB endpoint.
func (c *Client) Healthz(ctx context.Context) error {
	return baseclientset.NewWithResty(c.client).Healthz(ctx)
}

// NewClient creates a new CMDB client with the given endpoint and optional AK/SK credentials.
func NewClient(endpoint, secretID, secretKey string) Interface {
	client := &Client{
		endpoint:  endpoint,
		apiKey:    secretID,
		apiSecret: secretKey,
	}

	client.client = client.newRestyClient(endpoint + "/cmdbapi")

	return client
}

// newRestyClient creates a resty client with the given base URL and wires up signing/logging middleware.
func (c *Client) newRestyClient(baseURL string) *resty.Client {
	rc := resty.New().
		SetBaseURL(baseURL).
		SetTimeout(30*time.Second).
		SetHeader("Content-Type", "application/json")

	rc.AddRequestMiddleware(func(_ *resty.Client, req *resty.Request) error {
		return c.signRequest(req)
	})

	rc.AddResponseMiddleware(func(_ *resty.Client, resp *resty.Response) error {
		req := resp.Request
		slog.Debug("CMDB: Request completed",
			"method", req.Method,
			"url", req.URL,
			"status_code", resp.StatusCode(),
			"response_time_ms", resp.Duration().Milliseconds(),
		)
		if resp.StatusCode() >= 400 {
			slog.Error("CMDB: Request failed",
				"status_code", resp.StatusCode(),
				"response_body", string(resp.Bytes()[:min(len(resp.Bytes()), 500)]),
				"url", req.URL,
				"method", req.Method,
			)
		}
		return nil
	})

	return rc
}

// signRequest computes and injects the four AK/SK headers into req.
func (c *Client) signRequest(req *resty.Request) error {
	if c.apiKey == "" {
		// No credentials configured – skip signing (e.g. local dev).
		slog.Debug("CMDB: Skipping signing - no API key configured")
		return nil
	}

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := uuid.New().String()

	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}

	path := ""
	if req.URL != "" {
		if parsed, err := url.Parse(req.URL); err == nil {
			path = parsed.Path
		} else {
			path = req.URL
			slog.Warn("CMDB: URL parse failed, using raw URL as path",
				"raw_url", req.URL,
				"error", err,
			)
		}
	}

	// The resty middleware runs before the base URL is prepended, so req.URL only contains
	// the relative path. The server verifies against the full path including /cmdbapi.
	if path != "" && !strings.HasPrefix(path, "/cmdbapi") {
		path = "/cmdbapi" + path
	}

	if path == "" {
		slog.Error("CMDB: PATH is empty after parsing - signature will be invalid",
			"req.URL", req.URL,
			"method", method,
		)
	}

	body := req.Body
	var bodyBytes []byte
	switch v := body.(type) {
	case []byte:
		bodyBytes = v
	case string:
		bodyBytes = []byte(v)
	case nil:
		bodyBytes = []byte{}
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("cmdb sign: marshal body: %w", err)
		}
		bodyBytes = encoded
	}

	//nolint:gosec // MD5 is mandated by the CMDB signing spec
	h := md5.Sum(bodyBytes)
	bodyMd5 := hex.EncodeToString(h[:])

	raw := strings.Join([]string{method, path, bodyMd5, timestamp, nonce}, "\n")

	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte(raw))
	sign := hex.EncodeToString(mac.Sum(nil))

	req.SetHeader("X-API-KEY", c.apiKey)
	req.SetHeader("X-TIMESTAMP", timestamp)
	req.SetHeader("X-NONCE", nonce)
	req.SetHeader("X-SIGN", sign)

	return nil
}

// TreeSearchResponse represents the response from tree search API
type TreeSearchResponse struct {
	Success   bool       `json:"success"`
	Data      []TreeNode `json:"data"`
	Timestamp string     `json:"timestamp"`
}

// TreeNode represents a node in the CMDB tree structure
type TreeNode struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	DisplayType string     `json:"display_type"`
	IsLeaf      bool       `json:"is_leaf"`
	Children    []TreeNode `json:"children"`
	Status      int        `json:"status"`
}

// ResourceResponse represents the response from resource query API
type ResourceResponse struct {
	Success   bool         `json:"success"`
	Data      ResourceData `json:"data"`
	Timestamp string       `json:"timestamp"`
}

// ResourceData contains paginated resource rows
type ResourceData struct {
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
	Total    int          `json:"total"`
	Rows     [][]KeyValue `json:"rows"`
}

// KeyValue represents a key-value pair in resource data.
type KeyValue struct {
	ID    string `json:"id,omitempty"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// StringValue returns Value as a plain string (convenience alias).
func (kv KeyValue) StringValue() string {
	return kv.Value
}

// flexKeyValue is used only for the service-tree GET API where some value fields
// may be integers.
type flexKeyValue struct {
	ID    string      `json:"id,omitempty"`
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

// stringVal safely converts an interface{} value to string.
func stringVal(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// flexResourceResponse is the response type for the service-tree associated resource API.
type flexResourceResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Page     int              `json:"page"`
		PageSize int              `json:"page_size"`
		Total    int              `json:"total"`
		Rows     [][]flexKeyValue `json:"rows"`
	} `json:"data"`
	Timestamp string `json:"timestamp"`
}

// VPCInfo contains VPC information from CMDB
type VPCInfo struct {
	Name string
	ID   string
	CIDR string
}

// SubnetInfo contains subnet information from CMDB
type SubnetInfo struct {
	Name             string
	ID               string
	CIDR             string
	AvailabilityZone string
}

// SecurityGroupInfo contains security group information from CMDB
type SecurityGroupInfo struct {
	Name        string
	ID          string
	Description string
}

// NetworkResources contains all network-related resources for a node
type NetworkResources struct {
	VPC            *VPCInfo
	Subnets        []*SubnetInfo
	SecurityGroups []*SecurityGroupInfo
}

// BusinessServiceInfo contains business service information from service tree
type BusinessServiceInfo struct {
	ServiceID    string
	ServiceName  string
	BusinessLine string
}

// AWSRoleInfo contains AWS IAM role information
type AWSRoleInfo struct {
	RoleName string
	RoleARN  string
}

// ServiceSummaryResponse represents the response from service summary API
type ServiceSummaryResponse struct {
	Success   bool                  `json:"success"`
	Data      []ServiceSummaryGroup `json:"data"`
	Timestamp string                `json:"timestamp"`
}

// ServiceSummaryGroup represents a group of attributes in service summary
type ServiceSummaryGroup struct {
	GroupName string               `json:"group_name"`
	Attrs     []ServiceSummaryAttr `json:"attrs"`
}

// ServiceSummaryAttr represents an attribute in service summary
type ServiceSummaryAttr struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	ID     string `json:"id,omitempty"`
	IsLink bool   `json:"is_link,omitempty"`
}

// SearchPodNode searches for a pod in CMDB tree and returns its node ID.
func (c *Client) SearchPodNode(ctx context.Context, podName, clusterName, namespace string) (string, error) {
	var result TreeSearchResponse

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"search": podName,
			"env":    "all",
			"rtype":  "basic_resource",
		}).
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/search", ViewEKS))

	if err != nil {
		return "", fmt.Errorf("failed to search pod node: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return "", fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return "", fmt.Errorf("CMDB search failed")
	}

	nodeID := findPodNodeID(result.Data, podName, clusterName, namespace)
	if nodeID == "" {
		return "", fmt.Errorf("pod node not found in CMDB tree")
	}

	return nodeID, nil
}

// findPodNodeID recursively searches for the pod node in the tree.
func findPodNodeID(nodes []TreeNode, podName, clusterName, namespace string) string {
	var traverse func([]TreeNode, string, string) string
	traverse = func(nodes []TreeNode, currentCluster, currentNamespace string) string {
		for _, node := range nodes {
			if node.Type == "aws_eks_cluster" {
				currentCluster = node.Name
			}

			if node.Type == "aws_eks_namespace" {
				currentNamespace = node.Name
			}

			if node.Type == "aws_eks_pod" && node.Name == podName {
				if clusterName != "" && currentCluster != clusterName {
					continue
				}
				if namespace != "" && currentNamespace != namespace {
					continue
				}
				return node.ID
			}

			if len(node.Children) > 0 {
				if id := traverse(node.Children, currentCluster, currentNamespace); id != "" {
					return id
				}
			}
		}
		return ""
	}

	return traverse(nodes, "", "")
}

// GetNetworkResources retrieves all network-related resources for a node.
func (c *Client) GetNetworkResources(ctx context.Context, viewID ViewID, nodeID string) (*NetworkResources, error) {
	resources := &NetworkResources{}

	type resourceResult struct {
		resourceType ResourceType
		rows         [][]KeyValue
		err          error
	}

	resultChan := make(chan resourceResult, 3)

	go func() {
		rows, err := c.queryAssociatedResources(ctx, viewID, nodeID, ResourceTypeVPC)
		resultChan <- resourceResult{ResourceTypeVPC, rows, err}
	}()

	go func() {
		rows, err := c.queryAssociatedResources(ctx, viewID, nodeID, ResourceTypeSubnet)
		resultChan <- resourceResult{ResourceTypeSubnet, rows, err}
	}()

	go func() {
		rows, err := c.queryAssociatedResources(ctx, viewID, nodeID, ResourceTypeSecurityGroup)
		resultChan <- resourceResult{ResourceTypeSecurityGroup, rows, err}
	}()

	for i := 0; i < 3; i++ {
		result := <-resultChan

		if result.err != nil {
			slog.ErrorContext(ctx, "queryAssociatedResources failed", "error", result.err, "resource_type", result.resourceType, "view_id", viewID, "node_id", nodeID)
			continue
		}

		switch result.resourceType {
		case ResourceTypeVPC:
			if len(result.rows) > 0 {
				vpc := &VPCInfo{}
				for _, kv := range result.rows[0] {
					switch kv.Key {
					case "vpc_name":
						vpc.Name = kv.Value
					case "aws_vpc_id":
						vpc.ID = kv.Value
					case "cidr_block":
						vpc.CIDR = kv.Value
					}
				}
				resources.VPC = vpc
			}

		case ResourceTypeSubnet:
			subnets := make([]*SubnetInfo, 0, len(result.rows))
			for _, row := range result.rows {
				subnet := &SubnetInfo{}
				for _, kv := range row {
					switch kv.Key {
					case "subnet_name":
						subnet.Name = kv.Value
					case "aws_subnet_id":
						subnet.ID = kv.Value
					case "cidr_block":
						subnet.CIDR = kv.Value
					case "availability_zone":
						subnet.AvailabilityZone = kv.Value
					}
				}
				subnets = append(subnets, subnet)
			}
			resources.Subnets = subnets

		case ResourceTypeSecurityGroup:
			sgs := make([]*SecurityGroupInfo, 0, len(result.rows))
			for _, row := range result.rows {
				sg := &SecurityGroupInfo{}
				for _, kv := range row {
					switch kv.Key {
					case "sg_name":
						sg.Name = kv.Value
					case "sg_id":
						sg.ID = kv.Value
					case "description":
						sg.Description = kv.Value
					}
				}
				sgs = append(sgs, sg)
			}
			resources.SecurityGroups = sgs
		}
	}

	return resources, nil
}

// queryAssociatedResources queries associated resources for a given node.
func (c *Client) queryAssociatedResources(ctx context.Context, viewID ViewID, nodeID string, resourceType ResourceType) ([][]KeyValue, error) {
	var result ResourceResponse

	requestBody := map[string]interface{}{
		"resource_type": resourceType,
		"page":          1,
		"page_size":     100,
		"search":        "",
	}

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParam("rtype", "basic_resource").
		SetHeader("Content-Type", "application/json").
		SetBody(requestBody).
		SetResult(&result).
		Post(fmt.Sprintf("/api/v1/tree/%d/node/%s/list-card-rows/associated-resources", viewID, nodeID))

	if err != nil {
		return nil, fmt.Errorf("failed to query %s: %w", resourceType, err)
	}

	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return nil, fmt.Errorf("CMDB query failed for %s", resourceType)
	}

	return result.Data.Rows, nil
}

// SearchBusinessService searches for a business service in the service tree.
func (c *Client) SearchBusinessService(ctx context.Context, serviceName, env string) (*BusinessServiceInfo, error) {
	var result TreeSearchResponse

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"search": serviceName,
			"env":    env,
			"rtype":  "stree",
		}).
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/search", ViewDeptTree))

	if err != nil {
		return nil, fmt.Errorf("failed to search business service: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success || len(result.Data) == 0 {
		return nil, fmt.Errorf("business service not found")
	}

	businessLine, serviceID, err := c.extractBusinessServiceInfo(ctx, result.Data, serviceName)
	if err != nil {
		return nil, err
	}
	if serviceID == "" {
		return nil, fmt.Errorf("service not found in tree")
	}

	return &BusinessServiceInfo{
		ServiceID:    serviceID,
		ServiceName:  serviceName,
		BusinessLine: businessLine,
	}, nil
}

// extractBusinessServiceInfo recursively extracts business line path and service ID.
func (c *Client) extractBusinessServiceInfo(ctx context.Context, nodes []TreeNode, targetServiceName string) (string, string, error) {
	var businessLineParts []string
	var serviceID string

	var traverse func([]TreeNode, []string) (bool, error)
	traverse = func(nodes []TreeNode, path []string) (bool, error) {
		for _, node := range nodes {
			if node.DisplayType == "主体" || node.DisplayType == "业务线" {
				newPath := append(path, node.Name)

				if len(node.Children) > 0 {
					found, err := traverse(node.Children, newPath)
					if err != nil {
						return false, err
					}
					if found {
						return true, nil
					}
				}
			} else if node.Type == "business_services" && node.Name == targetServiceName {
				businessLineParts = path
				serviceID = node.ID
				return true, nil
			} else if len(node.Children) > 0 {
				found, err := traverse(node.Children, path)
				if err != nil {
					return false, err
				}
				if found {
					return true, nil
				}
			}
		}
		return false, nil
	}

	_, err := traverse(nodes, []string{})
	if err != nil {
		return "", "", err
	}

	businessLine := ""
	if len(businessLineParts) > 0 {
		businessLine = businessLineParts[0]
		for i := 1; i < len(businessLineParts); i++ {
			businessLine += " > " + businessLineParts[i]
		}
	}

	return businessLine, serviceID, nil
}

// GetNodeSummary retrieves summary information for any node type.
func (c *Client) GetNodeSummary(ctx context.Context, viewID ViewID, nodeID string) (*ServiceSummaryResponse, error) {
	var result ServiceSummaryResponse

	resp, err := c.client.R().
		SetContext(ctx).
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/node/%s/summary", viewID, nodeID))

	if err != nil {
		return nil, fmt.Errorf("failed to get node summary: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return nil, fmt.Errorf("failed to get node summary")
	}

	return &result, nil
}

// queryServiceAssociatedResource is the shared underlying implementation for
// GetServiceAWSRoles.
func (c *Client) queryServiceAssociatedResource(ctx context.Context, serviceID, env string, resourceType ResourceType) ([][]flexKeyValue, error) {
	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"env":           env,
			"rtype":         "stree",
			"page":          "1",
			"page_size":     "100",
			"search":        "",
			"resource_type": string(resourceType),
		}).
		Get(fmt.Sprintf("/api/v1/tree/%d/node/%s/associated/resource/rows", ViewDeptTree, serviceID))

	if err != nil {
		return nil, fmt.Errorf("failed to query %s for service %s: %w", resourceType, serviceID, err)
	}
	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	var result flexResourceResponse
	if err := json.Unmarshal(resp.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("failed to parse response for %s: %w", resourceType, err)
	}
	if !result.Success {
		return nil, fmt.Errorf("CMDB query failed for %s", resourceType)
	}

	return result.Data.Rows, nil
}

// GetServiceAWSRoles retrieves AWS IAM roles associated with a service via service tree.
func (c *Client) GetServiceAWSRoles(ctx context.Context, serviceID, env string) ([]*AWSRoleInfo, error) {
	rows, err := c.queryServiceAssociatedResource(ctx, serviceID, env, ResourceTypeAWSRole)
	if err != nil {
		return nil, err
	}

	roles := make([]*AWSRoleInfo, 0, len(rows))
	for _, row := range rows {
		role := &AWSRoleInfo{}
		for _, kv := range row {
			switch kv.Key {
			case "role_name":
				role.RoleName = stringVal(kv.Value)
			case "role_arn":
				role.RoleARN = stringVal(kv.Value)
			}
		}
		if role.RoleName != "" {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

// SearchEC2Instance searches for EC2 instances in CMDB tree and returns matching nodes.
func (c *Client) SearchEC2Instance(ctx context.Context, searchTerm string) ([]TreeNode, error) {
	var result TreeSearchResponse

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"search": searchTerm,
			"env":    "all",
			"rtype":  "basic_resource",
		}).
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/search", ViewEC2))

	if err != nil {
		return nil, fmt.Errorf("failed to search EC2 instance: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return nil, fmt.Errorf("CMDB search failed")
	}

	instances := findComputeInstances(result.Data)
	if len(instances) == 0 {
		return nil, fmt.Errorf("no EC2 instances found")
	}

	return instances, nil
}

// findComputeInstances recursively searches for compute_instance nodes.
func findComputeInstances(nodes []TreeNode) []TreeNode {
	var instances []TreeNode

	var traverse func([]TreeNode)
	traverse = func(nodes []TreeNode) {
		for _, node := range nodes {
			if node.Type == "compute_instance" {
				instances = append(instances, node)
			}
			if len(node.Children) > 0 {
				traverse(node.Children)
			}
		}
	}

	traverse(nodes)
	return instances
}

// GetEC2InstanceDetails retrieves detailed attributes for an EC2 instance.
func (c *Client) GetEC2InstanceDetails(ctx context.Context, nodeID string) (map[string]map[string]string, error) {
	var result ServiceSummaryResponse

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParam("rtype", "basic_resource").
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/node/%s/directory/attr", ViewEC2, nodeID))

	if err != nil {
		return nil, fmt.Errorf("failed to get EC2 instance details: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return nil, fmt.Errorf("failed to get EC2 instance details")
	}

	details := make(map[string]map[string]string)
	for _, group := range result.Data {
		groupAttrs := make(map[string]string)
		for _, attr := range group.Attrs {
			groupAttrs[attr.Label] = attr.Value
		}
		details[group.GroupName] = groupAttrs
	}

	return details, nil
}

// ServiceClusterInfo contains K8s cluster info associated with a service.
type ServiceClusterInfo struct {
	ServiceClusterID int    `json:"service_cluster_id"`
	ClusterName      string `json:"cluster_name"`
	Namespace        string `json:"namespace"`
	EnvCode          string `json:"env_code"`
	EnvName          string `json:"env_name"`
	ServiceType      string `json:"service_type"`
}

// ServiceByIPService contains service info returned by the service-by-ip API.
type ServiceByIPService struct {
	ServiceID       int                  `json:"service_id"`
	ServiceCode     string               `json:"service_code"`
	ServiceName     string               `json:"service_name"`
	ServiceType     string               `json:"service_type"`
	ServicePurpose  string               `json:"service_purpose"`
	Level           string               `json:"level"`
	Language        string               `json:"language"`
	Port            string               `json:"port"`
	Users           any                  `json:"users"`
	Status          int                  `json:"status"`
	ServiceClusters []ServiceClusterInfo `json:"service_clusters"`
}

// ServiceByIPItem is one match entry, grouped by IP type.
type ServiceByIPItem struct {
	IP        string `json:"ip"`
	IPType    string `json:"ip_type"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
	// EKS fields
	EKSClusterName string `json:"eks_cluster_name,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	PodName        string `json:"pod_name,omitempty"`
	// EC2 fields
	InstanceID   string               `json:"instance_id,omitempty"`
	InstanceName string               `json:"instance_name,omitempty"`
	Services     []ServiceByIPService `json:"services"`
}

// ServiceByIPResponse is the top-level response from GET /svc/service-by-ip.
type ServiceByIPResponse struct {
	IP    string            `json:"ip"`
	Total int               `json:"total"`
	Items []ServiceByIPItem `json:"items"`
}

// ServiceNetworkVPC contains VPC info associated with a service environment.
type ServiceNetworkVPC struct {
	VPCID     string `json:"vpc_id"`
	VPCName   string `json:"vpc_name"`
	CIDRBlock string `json:"cidr_block"`
	IsDefault bool   `json:"is_default"`
}

// ServiceNetworkSubnet contains subnet info associated with a service environment.
type ServiceNetworkSubnet struct {
	SubnetID         string `json:"subnet_id"`
	SubnetName       string `json:"subnet_name"`
	VPCID            string `json:"vpc_id"`
	CIDRBlock        string `json:"cidr_block"`
	AvailabilityZone string `json:"availability_zone"`
}

// ServiceNetworkSecurityGroup contains security group info associated with a service environment.
type ServiceNetworkSecurityGroup struct {
	SecurityGroupID   string `json:"security_group_id"`
	SecurityGroupName string `json:"security_group_name"`
	VPCID             string `json:"vpc_id"`
	Description       string `json:"description"`
}

// ServiceNetworkEC2Instance contains EC2 instance info associated with a service environment.
type ServiceNetworkEC2Instance struct {
	EC2InstanceID string `json:"ec2_instance_id"`
	InstanceID    string `json:"instance_id"`
	InstanceName  string `json:"instance_name"`
	InstanceType  string `json:"instance_type"`
	InstanceState string `json:"instance_state"`
	PrivateIP     string `json:"private_ip"`
	PublicIP      string `json:"public_ip"`
	SubnetID      string `json:"subnet_id"`
	VPCID         string `json:"vpc_id"`
	AZ            string `json:"availability_zone"`
}

// ServiceNetworkPod contains Pod info associated with a service environment.
type ServiceNetworkPod struct {
	PodID         int    `json:"pod_id"`
	PodName       string `json:"pod_name"`
	PodIP         string `json:"pod_ip"`
	HostIP        string `json:"host_ip"`
	Phase         string `json:"phase"`
	ClusterName   string `json:"cluster_name"`
	NamespaceName string `json:"namespace_name"`
}

// ServiceNetworkIAMRole contains IAM role info associated with a service environment.
type ServiceNetworkIAMRole struct {
	RoleID      int    `json:"role_id"`
	RoleName    string `json:"role_name"`
	RoleARN     string `json:"role_arn"`
	Description string `json:"description"`
}

// ServiceNetworkInfo is the response from GET /aws/network/service-env-association.
type ServiceNetworkInfo struct {
	Account        string                        `json:"account"`
	Region         string                        `json:"region"`
	ResourceID     string                        `json:"resource_id"`
	ResourceName   string                        `json:"resource_name"`
	ResourceType   string                        `json:"resource_type"`
	VPCs           []ServiceNetworkVPC           `json:"vpcs"`
	Subnets        []ServiceNetworkSubnet        `json:"subnets"`
	SecurityGroups []ServiceNetworkSecurityGroup `json:"security_groups"`
	EC2Instances   []ServiceNetworkEC2Instance   `json:"ec2_instances,omitempty"`
	Pods           []ServiceNetworkPod           `json:"pods,omitempty"`
	CIRoles        []ServiceNetworkIAMRole       `json:"ci_roles,omitempty"`
	CDRoles        []ServiceNetworkIAMRole       `json:"cd_roles,omitempty"`
}

// GetServiceNetworkInfo queries VPC, subnet, security group, EC2, Pod and IAM role info
// associated with a service in the given environment.
func (c *Client) GetServiceNetworkInfo(ctx context.Context, serviceName, envCodeAlias string) (*ServiceNetworkInfo, error) {
	var envelope struct {
		Success bool                `json:"success"`
		Data    *ServiceNetworkInfo `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details string `json:"details"`
		} `json:"error,omitempty"`
	}

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"serviceName": serviceName,
			"env":         envCodeAlias,
		}).
		SetResult(&envelope).
		Get("/ext/api/v1/aws/network/service-env-association")

	if err != nil {
		return nil, fmt.Errorf("cmdb get service network %s/%s: %w", serviceName, envCodeAlias, err)
	}
	if !resp.IsStatusSuccess() {
		message := fmt.Sprintf("http %d", resp.StatusCode())
		if envelope.Error != nil {
			detail := envelope.Error.Message
			if envelope.Error.Details != "" {
				detail = envelope.Error.Details
			}
			message = detail
		}

		return nil, fmt.Errorf("cmdb get service network %s/%s: %s", serviceName, envCodeAlias, message)
	}
	if !envelope.Success {
		message := "unknown error"
		if envelope.Error != nil {
			detail := envelope.Error.Message
			if envelope.Error.Details != "" {
				detail = envelope.Error.Details
			}
			message = detail
		}
		return nil, fmt.Errorf("cmdb get service network %s/%s: %s", serviceName, envCodeAlias, message)
	}
	if envelope.Data == nil {
		return &ServiceNetworkInfo{}, nil
	}
	return envelope.Data, nil
}

// ServiceLocation aggregates the AWS account, Kubernetes cluster(s) and
// namespace(s) that a service runs in for a given environment.
type ServiceLocation struct {
	ServiceName string   `json:"service_name"`
	Env         string   `json:"env"`
	Account     string   `json:"account"`
	Region      string   `json:"region"`
	Clusters    []string `json:"clusters"`
	Namespaces  []string `json:"namespaces"`
	Pods        []string `json:"pods"`
}

// GetServiceLocation resolves the AWS account, Kubernetes cluster(s) and
// namespace(s) for the given service in the given environment by querying CMDB.
func (c *Client) GetServiceLocation(ctx context.Context, serviceName, envCodeAlias string) (*ServiceLocation, error) {
	info, err := c.GetServiceNetworkInfo(ctx, serviceName, envCodeAlias)
	if err != nil {
		return nil, fmt.Errorf("cmdb get service location %s/%s: %w", serviceName, envCodeAlias, err)
	}

	loc := &ServiceLocation{
		ServiceName: serviceName,
		Env:         envCodeAlias,
		Account:     info.Account,
		Region:      info.Region,
	}

	seenCluster := make(map[string]bool, len(info.Pods))
	seenNamespace := make(map[string]bool, len(info.Pods))
	for _, pod := range info.Pods {
		if pod.ClusterName != "" && !seenCluster[pod.ClusterName] {
			seenCluster[pod.ClusterName] = true
			loc.Clusters = append(loc.Clusters, pod.ClusterName)
		}
		if pod.NamespaceName != "" && !seenNamespace[pod.NamespaceName] {
			seenNamespace[pod.NamespaceName] = true
			loc.Namespaces = append(loc.Namespaces, pod.NamespaceName)
		}
		if pod.PodName != "" {
			loc.Pods = append(loc.Pods, pod.PodName)
		}
	}

	return loc, nil
}

// GetServiceByIP queries the service and resource info associated with an IP address.
func (c *Client) GetServiceByIP(ctx context.Context, ip string) (*ServiceByIPResponse, error) {
	var envelope struct {
		Success bool                 `json:"success"`
		Data    *ServiceByIPResponse `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParam("ip", ip).
		SetResult(&envelope).
		Get("/ext/api/v1/svc/service-by-ip")

	if err != nil {
		return nil, fmt.Errorf("cmdb get service by ip %s: %w", ip, err)
	}
	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("cmdb get service by ip %s: http %d", ip, resp.StatusCode())
	}
	if !envelope.Success {
		message := "unknown error"
		if envelope.Error != nil {
			message = envelope.Error.Message
		}
		return nil, fmt.Errorf("cmdb get service by ip %s: %s", ip, message)
	}
	if envelope.Data == nil {
		return &ServiceByIPResponse{IP: ip}, nil
	}
	return envelope.Data, nil
}

// GetEC2AssociatedServices retrieves associated business services for an EC2 instance.
func (c *Client) GetEC2AssociatedServices(ctx context.Context, nodeID string) ([]*BusinessServiceInfo, error) {
	var result ResourceResponse

	requestBody := map[string]interface{}{
		"env":       "all",
		"page":      1,
		"page_size": 100,
		"filters":   map[string]interface{}{},
	}

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParam("rtype", "basic_resource").
		SetHeader("Content-Type", "application/json").
		SetBody(requestBody).
		SetResult(&result).
		Post(fmt.Sprintf("/api/v1/tree/%d/node/%s/table-rows/associated-services", ViewEC2, nodeID))

	if err != nil {
		return nil, fmt.Errorf("failed to get associated services: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return nil, fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return nil, fmt.Errorf("failed to get associated services")
	}

	services := make([]*BusinessServiceInfo, 0, len(result.Data.Rows))
	for _, row := range result.Data.Rows {
		service := &BusinessServiceInfo{}
		var unitName, deptName string

		for _, kv := range row {
			switch kv.Key {
			case "service_name":
				service.ServiceName = kv.Value
			case "unit_name":
				unitName = kv.Value
			case "department_name":
				deptName = kv.Value
			}
			if kv.ID != "" && service.ServiceID == "" {
				service.ServiceID = kv.ID
			}
		}

		if unitName != "" && deptName != "" {
			service.BusinessLine = fmt.Sprintf("%s > %s", unitName, deptName)
		} else if unitName != "" {
			service.BusinessLine = unitName
		} else if deptName != "" {
			service.BusinessLine = deptName
		}

		if service.ServiceName != "" {
			services = append(services, service)
		}
	}

	return services, nil
}

// TreeNodeResponse is the response type for service-cluster tree queries.
type TreeNodeResponse struct {
	Success   bool   `json:"success"`
	Data      []Node `json:"data"`
	Timestamp string `json:"timestamp"`
}

// Node is a service-cluster tree node.
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Children []Node `json:"children"`
}

// GetServiceClusterId searches the service tree and returns the serviceclusterid via envAlias.
func (c *Client) GetServiceClusterId(ctx context.Context, serviceName, envAlias string) (string, error) {
	var result TreeNodeResponse

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"search": serviceName,
			"env":    "all",
			"rtype":  "stree",
		}).
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/search", ViewDeptTree))

	if err != nil {
		return "", fmt.Errorf("failed to get serviceclustertree: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return "", fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return "", fmt.Errorf("failed to get serviceclustertree")
	}

	serviceClusterId, find := FindServiceClusterID(result.Data, envAlias)
	if !find {
		return "", fmt.Errorf("did not find serviceclustertree")
	}

	return serviceClusterId, nil
}

// FindServiceClusterID recursively finds a service_clusters node by name.
func FindServiceClusterID(nodes []Node, target string) (string, bool) {
	for _, node := range nodes {
		if node.Type == "service_clusters" && node.Name == target {
			return node.ID, true
		}

		if len(node.Children) > 0 {
			if id, ok := FindServiceClusterID(node.Children, target); ok {
				return id, true
			}
		}
	}

	return "", false
}

// Response is the envelope for associated-resource-rows queries.
type Response struct {
	Success   bool   `json:"success"`
	Data      Data   `json:"data"`
	Timestamp string `json:"timestamp"`
}

// Data contains paginated rows of fields.
type Data struct {
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
	Total    int       `json:"total"`
	Rows     [][]Field `json:"rows"`
}

// Field is a single key-value field.
type Field struct {
	ID    string `json:"id,omitempty"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PodInfo holds cluster/namespace/pod identity.
type PodInfo struct {
	Cluster   string
	Namespace string
	Pod       string
}

// GetEksClusterAndNamespace returns the cluster, namespace and pod of the first
// pod associated with the given service cluster.
func (c *Client) GetEksClusterAndNamespace(ctx context.Context, serviceClusterId string) (string, string, string, error) {
	var result Response

	resp, err := c.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"env":           "all",
			"rtype":         "stree",
			"page":          "1",
			"page_size":     "100",
			"search":        "",
			"resource_type": "aws_eks_pods",
		}).
		SetResult(&result).
		Get(fmt.Sprintf("/api/v1/tree/%d/node/%s/associated/resource/rows", ViewDeptTree, serviceClusterId))

	if err != nil {
		return "", "", "", fmt.Errorf("failed to get pod: %w", err)
	}

	if !resp.IsStatusSuccess() {
		return "", "", "", fmt.Errorf("CMDB API returned error: status=%d", resp.StatusCode())
	}

	if !result.Success {
		return "", "", "", fmt.Errorf("failed to get pod")
	}

	pods := ParsePods(result)
	if len(pods) == 0 {
		return "", "", "", fmt.Errorf("didnot to get pod")
	}
	return pods[0].Cluster, pods[0].Namespace, pods[0].Pod, nil
}

// ParsePods extracts pod identities from a Response.
func ParsePods(resp Response) []PodInfo {
	var result []PodInfo

	for _, row := range resp.Data.Rows {
		var pod PodInfo

		for _, field := range row {
			switch field.Key {
			case "cluster_name":
				pod.Cluster = field.Value

			case "namespace_name":
				pod.Namespace = field.Value

			case "pod_name":
				pod.Pod = field.Value
			}
		}

		result = append(result, pod)
	}

	return result
}

// CheckServicePermission is a placeholder implementation — see the
// CheckServicePermission doc comment on Interface for why.
func (c *Client) CheckServicePermission(_ context.Context, _, _ string, _ PermissionAction) (bool, error) {
	return false, ErrPermissionCheckNotImplemented
}
