package api

import (
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	pb "github.com/brocaar/lora-app-server/api"
	"github.com/brocaar/lora-app-server/internal/api/auth"
	"github.com/brocaar/lora-app-server/internal/config"
	"github.com/brocaar/lora-app-server/internal/storage"
	"github.com/brocaar/loraserver/api/ns"
	"github.com/brocaar/lorawan"
)

// GatewayAPI exports the Gateway related functions.
type GatewayAPI struct {
	validator auth.Validator
}

// NewGatewayAPI creates a new GatewayAPI.
func NewGatewayAPI(validator auth.Validator) *GatewayAPI {
	return &GatewayAPI{
		validator: validator,
	}
}

// Create creates the given gateway.
func (a *GatewayAPI) Create(ctx context.Context, req *pb.CreateGatewayRequest) (*pb.CreateGatewayResponse, error) {
	err := a.validator.Validate(ctx, auth.ValidateGatewaysAccess(auth.Create, req.OrganizationID))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	// also validate that the network-server is accessible for the given organization
	err = a.validator.Validate(ctx, auth.ValidateOrganizationNetworkServerAccess(auth.Read, req.OrganizationID, req.NetworkServerID))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	var mac lorawan.EUI64
	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad gateway mac: %s", err)
	}

	createReq := ns.CreateGatewayRequest{
		Mac:              mac[:],
		Name:             req.Name,
		Description:      req.Description,
		Latitude:         req.Latitude,
		Longitude:        req.Longitude,
		Altitude:         req.Altitude,
		GatewayProfileID: req.GatewayProfileID,
	}

	err = storage.Transaction(config.C.PostgreSQL.DB, func(tx sqlx.Ext) error {
		err = storage.CreateGateway(tx, &storage.Gateway{
			MAC:             mac,
			Name:            req.Name,
			Description:     req.Description,
			OrganizationID:  req.OrganizationID,
			Ping:            req.Ping,
			NetworkServerID: req.NetworkServerID,
		})
		if err != nil {
			return errToRPCError(err)
		}

		n, err := storage.GetNetworkServer(tx, req.NetworkServerID)
		if err != nil {
			return errToRPCError(err)
		}

		nsClient, err := config.C.NetworkServer.Pool.Get(n.Server, []byte(n.CACert), []byte(n.TLSCert), []byte(n.TLSKey))
		if err != nil {
			return errToRPCError(err)
		}

		_, err = nsClient.CreateGateway(ctx, &createReq)
		if err != nil && grpc.Code(err) != codes.AlreadyExists {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &pb.CreateGatewayResponse{}, nil
}

// Get returns the gateway matching the given Mac.
func (a *GatewayAPI) Get(ctx context.Context, req *pb.GetGatewayRequest) (*pb.GetGatewayResponse, error) {
	var mac lorawan.EUI64
	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad gateway mac: %s", err)
	}

	err := a.validator.Validate(ctx, auth.ValidateGatewayAccess(auth.Read, mac))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	gw, err := storage.GetGateway(config.C.PostgreSQL.DB, mac, false)
	if err != nil {
		return nil, errToRPCError(err)
	}

	n, err := storage.GetNetworkServer(config.C.PostgreSQL.DB, gw.NetworkServerID)
	if err != nil {
		return nil, errToRPCError(err)
	}

	nsClient, err := config.C.NetworkServer.Pool.Get(n.Server, []byte(n.CACert), []byte(n.TLSCert), []byte(n.TLSKey))
	if err != nil {
		return nil, errToRPCError(err)
	}

	getResp, err := nsClient.GetGateway(ctx, &ns.GetGatewayRequest{
		Mac: mac[:],
	})
	if err != nil {
		return nil, err
	}

	ret := &pb.GetGatewayResponse{
		Mac:              mac.String(),
		Name:             gw.Name,
		Description:      gw.Description,
		OrganizationID:   gw.OrganizationID,
		Ping:             gw.Ping,
		Latitude:         getResp.Latitude,
		Longitude:        getResp.Longitude,
		Altitude:         getResp.Altitude,
		CreatedAt:        gw.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:        gw.UpdatedAt.Format(time.RFC3339Nano),
		FirstSeenAt:      getResp.FirstSeenAt,
		LastSeenAt:       getResp.LastSeenAt,
		GatewayProfileID: getResp.GatewayProfileID,
		NetworkServerID:  gw.NetworkServerID,
	}
	return ret, err
}

// List lists the gateways.
func (a *GatewayAPI) List(ctx context.Context, req *pb.ListGatewayRequest) (*pb.ListGatewayResponse, error) {
	err := a.validator.Validate(ctx, auth.ValidateGatewaysAccess(auth.List, req.OrganizationID))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	var count int
	var gws []storage.Gateway

	if req.OrganizationID == 0 {
		isAdmin, err := a.validator.GetIsAdmin(ctx)
		if err != nil {
			return nil, errToRPCError(err)
		}

		if isAdmin {
			// in case of admin user list all gateways
			count, err = storage.GetGatewayCount(config.C.PostgreSQL.DB, req.Search)
			if err != nil {
				return nil, errToRPCError(err)
			}

			gws, err = storage.GetGateways(config.C.PostgreSQL.DB, int(req.Limit), int(req.Offset), req.Search)
			if err != nil {
				return nil, errToRPCError(err)
			}
		} else {
			// filter result based on user
			username, err := a.validator.GetUsername(ctx)
			if err != nil {
				return nil, errToRPCError(err)
			}
			count, err = storage.GetGatewayCountForUser(config.C.PostgreSQL.DB, username, req.Search)
			if err != nil {
				return nil, errToRPCError(err)
			}
			gws, err = storage.GetGatewaysForUser(config.C.PostgreSQL.DB, username, int(req.Limit), int(req.Offset), req.Search)
			if err != nil {
				return nil, errToRPCError(err)
			}
		}
	} else {
		count, err = storage.GetGatewayCountForOrganizationID(config.C.PostgreSQL.DB, req.OrganizationID, req.Search)
		if err != nil {
			return nil, errToRPCError(err)
		}
		gws, err = storage.GetGatewaysForOrganizationID(config.C.PostgreSQL.DB, req.OrganizationID, int(req.Limit), int(req.Offset), req.Search)
		if err != nil {
			return nil, errToRPCError(err)
		}
	}

	result := make([]*pb.ListGatewayItem, 0, len(gws))
	for i := range gws {
		result = append(result, &pb.ListGatewayItem{
			Mac:             gws[i].MAC.String(),
			Name:            gws[i].Name,
			Description:     gws[i].Description,
			CreatedAt:       gws[i].CreatedAt.Format(time.RFC3339Nano),
			UpdatedAt:       gws[i].UpdatedAt.Format(time.RFC3339Nano),
			OrganizationID:  gws[i].OrganizationID,
			NetworkServerID: gws[i].NetworkServerID,
		})
	}

	return &pb.ListGatewayResponse{
		TotalCount: int32(count),
		Result:     result,
	}, nil
}

// Update updates the given gateway.
func (a *GatewayAPI) Update(ctx context.Context, req *pb.UpdateGatewayRequest) (*pb.UpdateGatewayResponse, error) {
	var mac lorawan.EUI64
	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad gateway mac: %s", err)
	}

	err := a.validator.Validate(ctx, auth.ValidateGatewayAccess(auth.Update, mac))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	isAdmin, err := a.validator.GetIsAdmin(ctx)
	if err != nil {
		return nil, errToRPCError(err)
	}

	err = storage.Transaction(config.C.PostgreSQL.DB, func(tx sqlx.Ext) error {
		gw, err := storage.GetGateway(tx, mac, true)
		if err != nil {
			return errToRPCError(err)
		}

		gw.Name = req.Name
		gw.Description = req.Description
		gw.Ping = req.Ping
		if isAdmin {
			gw.OrganizationID = req.OrganizationID
		}

		err = storage.UpdateGateway(tx, &gw)
		if err != nil {
			return errToRPCError(err)
		}

		updateReq := ns.UpdateGatewayRequest{
			Mac:              mac[:],
			Name:             req.Name,
			Description:      req.Description,
			Latitude:         req.Latitude,
			Longitude:        req.Longitude,
			Altitude:         req.Altitude,
			GatewayProfileID: req.GatewayProfileID,
		}

		n, err := storage.GetNetworkServer(tx, gw.NetworkServerID)
		if err != nil {
			return errToRPCError(err)
		}

		nsClient, err := config.C.NetworkServer.Pool.Get(n.Server, []byte(n.CACert), []byte(n.TLSCert), []byte(n.TLSKey))
		if err != nil {
			return errToRPCError(err)
		}

		_, err = nsClient.UpdateGateway(ctx, &updateReq)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &pb.UpdateGatewayResponse{}, nil
}

// Delete deletes the gateway matching the given ID.
func (a *GatewayAPI) Delete(ctx context.Context, req *pb.DeleteGatewayRequest) (*pb.DeleteGatewayResponse, error) {
	var mac lorawan.EUI64
	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad gateway mac: %s", err)
	}

	err := a.validator.Validate(ctx, auth.ValidateGatewayAccess(auth.Delete, mac))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	err = storage.Transaction(config.C.PostgreSQL.DB, func(tx sqlx.Ext) error {
		err = storage.DeleteGateway(tx, mac)
		if err != nil {
			return errToRPCError(err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &pb.DeleteGatewayResponse{}, nil
}

// GetStats gets the gateway statistics for the gateway with the given Mac.
func (a *GatewayAPI) GetStats(ctx context.Context, req *pb.GetGatewayStatsRequest) (*pb.GetGatewayStatsResponse, error) {
	var mac lorawan.EUI64
	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad gateway mac: %s", err)
	}

	err := a.validator.Validate(ctx, auth.ValidateGatewayAccess(auth.Read, mac))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	gw, err := storage.GetGateway(config.C.PostgreSQL.DB, mac, false)
	if err != nil {
		return nil, errToRPCError(err)
	}

	n, err := storage.GetNetworkServer(config.C.PostgreSQL.DB, gw.NetworkServerID)
	if err != nil {
		return nil, errToRPCError(err)
	}

	nsClient, err := config.C.NetworkServer.Pool.Get(n.Server, []byte(n.CACert), []byte(n.TLSCert), []byte(n.TLSKey))
	if err != nil {
		return nil, errToRPCError(err)
	}

	interval, ok := ns.AggregationInterval_value[strings.ToUpper(req.Interval)]
	if !ok {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad interval: %s", req.Interval)
	}

	statsReq := ns.GetGatewayStatsRequest{
		Mac:            mac[:],
		Interval:       ns.AggregationInterval(interval),
		StartTimestamp: req.StartTimestamp,
		EndTimestamp:   req.EndTimestamp,
	}
	stats, err := nsClient.GetGatewayStats(ctx, &statsReq)
	if err != nil {
		return nil, err
	}

	result := make([]*pb.GatewayStats, len(stats.Result))
	for i, stat := range stats.Result {
		result[i] = &pb.GatewayStats{
			Timestamp:           stat.Timestamp,
			RxPacketsReceived:   stat.RxPacketsReceived,
			RxPacketsReceivedOK: stat.RxPacketsReceivedOK,
			TxPacketsReceived:   stat.TxPacketsReceived,
			TxPacketsEmitted:    stat.TxPacketsEmitted,
		}
	}

	return &pb.GetGatewayStatsResponse{
		Result: result,
	}, nil
}

// GetLastPing returns the last emitted ping and gateways receiving this ping.
func (a *GatewayAPI) GetLastPing(ctx context.Context, req *pb.GetLastPingRequest) (*pb.GetLastPingResponse, error) {
	var mac lorawan.EUI64
	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return nil, grpc.Errorf(codes.InvalidArgument, "bad gateway mac: %s", err)
	}

	err := a.validator.Validate(ctx, auth.ValidateGatewayAccess(auth.Read, mac))
	if err != nil {
		return nil, grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	ping, pingRX, err := storage.GetLastGatewayPingAndRX(config.C.PostgreSQL.DB, mac)
	if err != nil {
		return nil, errToRPCError(err)
	}

	resp := pb.GetLastPingResponse{
		CreatedAt: ping.CreatedAt.Format(time.RFC3339Nano),
		Frequency: uint32(ping.Frequency),
		Dr:        uint32(ping.DR),
	}

	for _, rx := range pingRX {
		resp.PingRX = append(resp.PingRX, &pb.PingRX{
			Mac:       rx.GatewayMAC.String(),
			Rssi:      int32(rx.RSSI),
			LoraSNR:   rx.LoRaSNR,
			Latitude:  rx.Location.Latitude,
			Longitude: rx.Location.Longitude,
			Altitude:  rx.Altitude,
		})
	}

	return &resp, nil
}

// StreamFrameLogs streams the uplink and downlink frame-logs for the given mac.
// Note: these are the raw LoRaWAN frames and this endpoint is intended for debugging.
func (a *GatewayAPI) StreamFrameLogs(req *pb.StreamGatewayFrameLogsRequest, srv pb.Gateway_StreamFrameLogsServer) error {
	var mac lorawan.EUI64

	if err := mac.UnmarshalText([]byte(req.Mac)); err != nil {
		return grpc.Errorf(codes.InvalidArgument, "mac: %s", err)
	}

	err := a.validator.Validate(srv.Context(), auth.ValidateGatewayAccess(auth.Read, mac))
	if err != nil {
		return grpc.Errorf(codes.Unauthenticated, "authentication failed: %s", err)
	}

	n, err := storage.GetNetworkServerForGatewayMAC(config.C.PostgreSQL.DB, mac)
	if err != nil {
		return errToRPCError(err)
	}

	nsClient, err := config.C.NetworkServer.Pool.Get(n.Server, []byte(n.CACert), []byte(n.TLSCert), []byte(n.TLSKey))
	if err != nil {
		return errToRPCError(err)
	}

	streamClient, err := nsClient.StreamFrameLogsForGateway(srv.Context(), &ns.StreamFrameLogsForGatewayRequest{
		Mac: mac[:],
	})
	if err != nil {
		return err
	}

	for {
		resp, err := streamClient.Recv()
		if err != nil {
			return err
		}

		up, down, err := convertUplinkAndDownlinkFrames(resp.UplinkFrames, resp.DownlinkFrames, false)
		if err != nil {
			return errToRPCError(err)
		}

		err = srv.Send(&pb.StreamGatewayFrameLogsResponse{
			UplinkFrames:   up,
			DownlinkFrames: down,
		})
		if err != nil {
			return err
		}
	}
}
