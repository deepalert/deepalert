package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/deepalert/deepalert"
	"github.com/deepalert/deepalert/internal/adaptor"
	"github.com/deepalert/deepalert/internal/errors"
	"github.com/deepalert/deepalert/internal/models"
	"github.com/google/uuid"
)

/*
	DynamoDB Design

	Data models
	- Alert : Generated by a security monitoring device. It has attribute(s).
	- Attribute : Values appeared in an Alert (e.g. IP address, domain name, user name, etc.)
	- Content : A result of attribute inspection by Inspector
	- Report : All results. It consists of Alert(S), Content(S) and a result of Reviewer.


	Keys
	- AlertID : Generated by Alert.Detector, Alert.RuneName and Alert.AlertKey.
	- ReportID : Assigned to unique AlertKey and time range. Same AlertID can have multiple
				ReportID if timestamps of alert are distant from each other.
	- AttrHash: Hashed value of an attribute, generated by all fields of Attribute.

	Primary/secondary key design (in "pk", "sk" field and stored data)
	- alertmap/{AlertID}, fixedkey -> ReportID
	- alert/{ReportID}, cache/{random} -> Alert(s)
	- content/{ReportID}, {AttrHash}/{Random} -> Content(S)
	- attribute/{ReportID}, {AttrHash} -> Attribute (for caching)
*/

// RepositoryService is interface of data repository. This is designed to be used with DynamoDB, but adaptor.Repository can be replaced with other repository. (e.g. mock.Repository)
type RepositoryService struct {
	repo adaptor.Repository
	ttl  time.Duration
}

// NewRepositoryService is constructor of RepositoryService. ttl is used to calculate ExpiresAt by now + ttl * time.Second
func NewRepositoryService(repo adaptor.Repository, ttl int64) *RepositoryService {
	return &RepositoryService{
		repo: repo,
		ttl:  time.Duration(ttl) * time.Second,
	}
}

// -----------------------------------------------------------
// Control alertEntry to manage AlertID to ReportID mapping
//

func newReportID() deepalert.ReportID {
	return deepalert.ReportID(uuid.New().String())
}

func (x *RepositoryService) TakeReport(alert deepalert.Alert, now time.Time) (*deepalert.Report, error) {
	fixedKey := "Fixed"
	alertID := alert.AlertID()

	entry := models.AlertEntry{
		RecordBase: models.RecordBase{
			PKey:      "alertmap/" + alertID,
			SKey:      fixedKey,
			ExpiresAt: now.UTC().Add(x.ttl).Unix(),
			CreatedAt: now.UTC().Unix(),
		},
		ReportID: newReportID(),
	}

	if err := x.repo.PutAlertEntry(&entry, now); err != nil {
		if x.repo.IsConditionalCheckErr(err) {
			existedEntry, err := x.repo.GetAlertEntry(entry.PKey, entry.SKey)
			if err != nil {
				return nil, errors.Wrap(err, "Fail to get cached reportID").With("AlertID", alertID)
			}

			return &deepalert.Report{
				ID:        existedEntry.ReportID,
				Status:    deepalert.StatusMore,
				CreatedAt: time.Unix(existedEntry.CreatedAt, 0),
			}, nil
		}

		return nil, errors.Wrap(err, "Fail to create new alert entry").
			With("AlertID", alertID).With("repo", x.repo)
	}

	return &deepalert.Report{
		ID:        entry.ReportID,
		Status:    deepalert.StatusNew,
		CreatedAt: now,
	}, nil
}

// -----------------------------------------------------------
// Control alertCache to manage published alert data
//

func toAlertCacheKey(reportID deepalert.ReportID) (string, string) {
	return fmt.Sprintf("alert/%s", reportID), "cache/" + uuid.New().String()
}

func (x *RepositoryService) SaveAlertCache(reportID deepalert.ReportID, alert deepalert.Alert, now time.Time) error {
	raw, err := json.Marshal(alert)
	if err != nil {
		return errors.Wrap(err, "Fail to marshal alert").With("alert", alert)
	}

	pk, sk := toAlertCacheKey(reportID)
	cache := &models.AlertCache{
		RecordBase: models.RecordBase{
			PKey:      pk,
			SKey:      sk,
			ExpiresAt: now.UTC().Add(x.ttl).Unix(),
		},
		AlertData: raw,
	}

	if err := x.repo.PutAlertCache(cache); err != nil {
		return err
	}

	return nil
}

func (x *RepositoryService) FetchAlertCache(reportID deepalert.ReportID) ([]*deepalert.Alert, error) {
	pk, _ := toAlertCacheKey(reportID)
	var alerts []*deepalert.Alert

	caches, err := x.repo.GetAlertCaches(pk)
	if err != nil {
		return nil, errors.Wrap(err, "GetAlertCaches").With("reportID", reportID)
	}

	for _, cache := range caches {
		var alert deepalert.Alert
		if err := json.Unmarshal(cache.AlertData, &alert); err != nil {
			return nil, errors.Wrap(err, "Fail to unmarshal alert").With("data", string(cache.AlertData))
		}
		alerts = append(alerts, &alert)
	}

	return alerts, nil
}

// -----------------------------------------------------------
// Control reportRecord to manage report contents by inspector
//

func toInspectReportKeys(reportID deepalert.ReportID, inspect *deepalert.InspectReport) (string, string) {
	pk := fmt.Sprintf("content/%s", reportID)
	sk := ""
	if inspect != nil {
		sk = fmt.Sprintf("%s/%s", inspect.Attribute.Hash(), uuid.New().String())
	}
	return pk, sk
}

func (x *RepositoryService) SaveInspectReport(section deepalert.InspectReport, now time.Time) error {
	raw, err := json.Marshal(section)
	if err != nil {
		return errors.Wrap(err, "Fail to marshal ReportSection").With("section", section)
	}

	pk, sk := toInspectReportKeys(section.ReportID, &section)
	record := &models.InspectorReportRecord{
		RecordBase: models.RecordBase{
			PKey:      pk,
			SKey:      sk,
			ExpiresAt: now.UTC().Add(x.ttl).Unix(),
		},
		Data: raw,
	}

	if err := x.repo.PutInspectorReport(record); err != nil {
		return errors.Wrap(err, "Fail to put report record")
	}

	return nil
}

func (x *RepositoryService) FetchInspectReport(reportID deepalert.ReportID) ([]*deepalert.ReportSection, error) {
	pk, _ := toInspectReportKeys(reportID, nil)

	records, err := x.repo.GetInspectorReports(pk)
	if err != nil {
		return nil, err
	}

	var reports []*deepalert.InspectReport
	for _, record := range records {
		var section deepalert.InspectReport
		if err := json.Unmarshal(record.Data, &section); err != nil {
			return nil, errors.Wrap(err, "Fail to unmarshal report content").
				With("record", record).
				With("data", string(record.Data))
		}

		reports = append(reports, &section)
	}

	sections, err := remapSection(reports)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to remap InspectReport")
	}
	return sections, nil
}

func remapSection(inspectReports []*deepalert.InspectReport) ([]*deepalert.ReportSection, error) {
	sections := map[string]*deepalert.ReportSection{}

	for _, ir := range inspectReports {
		hv := ir.Attribute.Hash()
		section, ok := sections[hv]
		if !ok {
			section = &deepalert.ReportSection{
				OriginAttr: &ir.Attribute,
			}
			sections[hv] = section
		}
		switch ir.Type {
		case deepalert.ContentHost:
			c, ok := ir.Content.(*deepalert.ReportHost)
			if !ok {
				return nil, errors.New("Can not cast content to deepalert.ReportHost")
			}
			section.Hosts = append(section.Hosts, c)

		case deepalert.ContentUser:
			c, ok := ir.Content.(*deepalert.ReportUser)
			if !ok {
				return nil, errors.New("Can not cast content to deepalert.ReportUser")
			}
			section.Users = append(section.Users, c)

		case deepalert.ContentBinary:
			c, ok := ir.Content.(*deepalert.ReportBinary)
			if !ok {
				return nil, errors.New("Can not cast content to deepalert.ReportHost")
			}
			section.Binaries = append(section.Binaries, c)
		}
	}

	var sectionList []*deepalert.ReportSection
	for _, section := range sections {
		sectionList = append(sectionList, section)
	}
	return sectionList, nil
}

// -----------------------------------------------------------
// Control attribute cache to prevent duplicated invocation of Inspector with same attribute
//

func toAttributeCacheKey(reportID deepalert.ReportID) string {
	return fmt.Sprintf("attribute/%s", reportID)
}

func toReportKey(reportID deepalert.ReportID) string {
	return fmt.Sprintf("report/%s", reportID)
}

// IsReportStreamEvent checks if the record has reportKey
func IsReportStreamEvent(record *events.DynamoDBEventRecord) bool {
	pk, ok := record.Change.Keys[models.DynamoPKeyName]
	if !ok {
		return false
	}
	return strings.HasPrefix(pk.String(), "report/")
}

// PutAttributeCache puts attributeCache to DB and returns true. If the attribute alrady exists,
// it returns false.
func (x *RepositoryService) PutAttributeCache(reportID deepalert.ReportID, attr deepalert.Attribute, now time.Time) (bool, error) {
	var ts time.Time
	if attr.Timestamp != nil {
		ts = *attr.Timestamp
	} else {
		ts = now
	}

	cache := &models.AttributeCache{
		RecordBase: models.RecordBase{
			PKey:      toAttributeCacheKey(reportID),
			SKey:      attr.Hash(),
			ExpiresAt: now.Add(x.ttl).Unix(),
		},
		Timestamp:   ts,
		AttrKey:     attr.Key,
		AttrType:    string(attr.Type),
		AttrValue:   attr.Value,
		AttrContext: attr.Context,
	}

	if err := x.repo.PutAttributeCache(cache, now); err != nil {
		if x.repo.IsConditionalCheckErr(err) {
			// The attribute already exists
			return false, nil
		}

		return false, errors.Wrap(err, "Fail to put attr cache").
			With("reportID", reportID).
			With("attr", attr)
	}

	return true, nil
}

// FetchAttributeCache retrieves all cached attribute from DB.
func (x *RepositoryService) FetchAttributeCache(reportID deepalert.ReportID) ([]*deepalert.Attribute, error) {
	pk := toAttributeCacheKey(reportID)

	caches, err := x.repo.GetAttributeCaches(pk)
	if err != nil {
		return nil, errors.Wrap(err, "Fail to retrieve attributeCache").With("reportID", reportID)
	}

	var attrs []*deepalert.Attribute
	for _, cache := range caches {
		attr := deepalert.Attribute{
			Type:      deepalert.AttrType(cache.AttrType),
			Key:       cache.AttrKey,
			Value:     cache.AttrValue,
			Context:   cache.AttrContext,
			Timestamp: &cache.Timestamp,
		}

		attrs = append(attrs, &attr)
	}

	return attrs, nil
}

// PutReport puts report with a key based on report.ID
func (x *RepositoryService) PutReport(report *deepalert.Report) error {
	pk := toReportKey(report.ID)
	if err := x.repo.PutReport(pk, report); err != nil {
		return err
	}
	return nil
}

// GetReport gets a report by a key based on report.ID
func (x *RepositoryService) GetReport(reportID deepalert.ReportID) (*deepalert.Report, error) {
	pk := toReportKey(reportID)
	report, err := x.repo.GetReport(pk)
	if err != nil {
		return nil, err
	}
	return report, nil
}
