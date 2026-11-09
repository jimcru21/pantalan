package nodeinfo

import (
	"context"
	"time"
)

// dfTimeout bounds a system/df call, which can take far longer than the
// others on a Node with a lot of data.
const dfTimeout = 60 * time.Second

// DockerVersion is the part of GET /version the Dashboard shows.
type DockerVersion struct {
	Version       string
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string
}

// Version reads GET /version, through the apiVersion a Summary
// negotiated.
func (d *Docker) Version(ctx context.Context, apiVersion string) (DockerVersion, error) {
	var v DockerVersion
	err := d.get(ctx, apiVersion, "/version", nil, &v)
	return v, err
}

// DiskUsage is Docker's disk use, by kind, from system/df.
type DiskUsage struct {
	Images, Containers, Volumes, BuildCache DiskUse
	// At is when Docker computed it.
	At time.Time
}

// DiskUse is one kind's count, active count and bytes.
type DiskUse struct {
	Total, Active     int
	Size, Reclaimable int64
}

// DiskUsage is system/df. It walks volumes and sizes layers, so it is
// slow and grows with the Node's data: callers share its answer
// (internal/httpapi does, for 60 s).
func (d *Docker) DiskUsage(ctx context.Context, apiVersion string) (DiskUsage, error) {
	// The answer is shared, so one caller going away doesn't cancel it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dfTimeout)
	defer cancel()
	var body dfBody
	if err := d.get(ctx, apiVersion, "/system/df", nil, &body); err != nil {
		return DiskUsage{}, err
	}
	usage := body.usage()
	usage.At = d.now()
	return usage, nil
}

// dfBody is a system/df response in either shape: the per-kind summaries
// API 1.52 added, or the item lists older daemons send (and newer ones
// may still send alongside).
type dfBody struct {
	ImageUsage      *dfSummary
	ContainerUsage  *dfSummary
	VolumeUsage     *dfSummary
	BuildCacheUsage *dfSummary

	LayersSize int64
	Images     []struct {
		Size, SharedSize int64
		Containers       int64
	}
	Containers []struct {
		SizeRw int64
		State  string
	}
	Volumes []struct {
		UsageData *struct{ Size, RefCount int64 }
	}
	BuildCache []struct {
		Size          int64
		InUse, Shared bool
	}
}

type dfSummary struct {
	ActiveCount, TotalCount int
	TotalSize, Reclaimable  int64
}

func (s *dfSummary) use() DiskUse {
	return DiskUse{Total: s.TotalCount, Active: s.ActiveCount, Size: s.TotalSize, Reclaimable: s.Reclaimable}
}

// usage prefers the summaries and works the legacy lists out the way the
// docker CLI does.
func (b dfBody) usage() DiskUsage {
	var u DiskUsage

	if b.ImageUsage != nil {
		u.Images = b.ImageUsage.use()
	} else {
		var used int64
		for _, img := range b.Images {
			if img.Containers > 0 {
				u.Images.Active++
				used += img.Size - max(img.SharedSize, 0)
			}
		}
		u.Images.Total = len(b.Images)
		u.Images.Size = b.LayersSize
		u.Images.Reclaimable = max(b.LayersSize-used, 0)
	}

	if b.ContainerUsage != nil {
		u.Containers = b.ContainerUsage.use()
	} else {
		for _, c := range b.Containers {
			u.Containers.Size += c.SizeRw
			if c.State == "running" {
				u.Containers.Active++
			} else {
				u.Containers.Reclaimable += c.SizeRw
			}
		}
		u.Containers.Total = len(b.Containers)
	}

	if b.VolumeUsage != nil {
		u.Volumes = b.VolumeUsage.use()
	} else {
		for _, v := range b.Volumes {
			if v.UsageData == nil {
				continue
			}
			size := max(v.UsageData.Size, 0) // -1 when not computed
			u.Volumes.Size += size
			if v.UsageData.RefCount > 0 {
				u.Volumes.Active++
			} else {
				u.Volumes.Reclaimable += size
			}
		}
		u.Volumes.Total = len(b.Volumes)
	}

	if b.BuildCacheUsage != nil {
		u.BuildCache = b.BuildCacheUsage.use()
	} else {
		for _, c := range b.BuildCache {
			u.BuildCache.Size += c.Size
			if c.InUse {
				u.BuildCache.Active++
			} else if !c.Shared {
				u.BuildCache.Reclaimable += c.Size
			}
		}
		u.BuildCache.Total = len(b.BuildCache)
	}
	return u
}
