package main

import (
	"database/sql"
	"fmt"
	"github.com/google/tflow2/nfserver"
	"github.com/lib/pq"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"time"
)

type Settings struct {
	Aggregation struct {
		Period uint64
	}
	Database struct {
		Host string
		Port int
		Name string
		User string
		Pass string
	}
	Netflow struct {
		Address string
		Threads int
	}
	Logging struct {
		File  string
		Level int
	}
}

type Iface struct {
	Name   string
	Host   string
	Id     uint32
	Sample uint64
}

type TrafficAttributes struct {
	iface     *Iface
	remoteASN uint32
	localASN  uint32
	ingress   bool
}

type DataPoint struct {
	time time.Time
	data map[TrafficAttributes]uint64
}

func readInterfaceConfig(filename string) ([]*Iface, error) {
	ifaces := make([]*Iface, 0)
	configFile, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("Unable to read config file %s: %v", filename, err)
	}

	err = yaml.Unmarshal(configFile, &ifaces)
	if err != nil {
		return nil, fmt.Errorf("Unable to parse yaml file: %v", err)
	}

	return ifaces, nil
}

func readSettings(filename string) (Settings, error) {
	settings := Settings{}
	settingsFile, err := ioutil.ReadFile(filename)
	if err != nil {
		return settings, fmt.Errorf("Unable to read config file: %v", err)
	}

	err = yaml.Unmarshal(settingsFile, &settings)
	if err != nil {
		return settings, fmt.Errorf("Unable to parse yaml file: %v", err)
	}

	return settings, nil
}

func connectDB(settings Settings, retry bool) (*sql.DB, error) {
	// connect to the DB
	dbsettings := settings.Database
	dbinfo := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		dbsettings.Host, dbsettings.Port, dbsettings.User, dbsettings.Pass, dbsettings.Name)
	for {
		db, err := sql.Open("postgres", dbinfo)
		if err == nil {
			// Open() worked, but are we really connected?
			err = db.Ping()
			if err == nil {
				// everything OK, return the DB
				if settings.Logging.Level >= 2 {
					log.Println("DB connection created.")
				}
				return db, nil
			}
		}
		if retry {
			// Ever tried? Ever failed? Try again. Fail again. Fail better.
			if settings.Logging.Level >= 1 {
				log.Println("Could not connect to DB: ", err)
				log.Println("Retrying in 30 seconds...")
			}
			time.Sleep(30 * time.Second)
			continue
		} else {
			return nil, err
		}
	}
}

func bulkinsert(settings Settings, db *sql.DB, point *DataPoint) error {
	// begin a transaction, do a bulk insert with a prepared
	// statement and commit the transaction
	txn, err := db.Begin()
	if err != nil {
		return fmt.Errorf("Begin DB transaction failed: ", err)
	}

	// prepare the insert format
	stmt, err := txn.Prepare(pq.CopyIn("traffic", "time", "bandwidth", "iface", "ingress", "remote_asn", "local_asn"))
	if err != nil {
		return fmt.Errorf("Praparing SQL statement failed: ", err)
	}

	// bulk insert
	for attr, size := range point.data {
		_, err = stmt.Exec(point.time, size/settings.Aggregation.Period, attr.iface.Name, attr.ingress, attr.remoteASN, attr.localASN)
		if err != nil {
			return fmt.Errorf("Error during bulk insert: ", err)
		}
	}

	_, err = stmt.Exec()
	if err != nil {
		return fmt.Errorf("Could not execute statement: %v", err)
	}
	err = stmt.Close()
	if err != nil {
		return fmt.Errorf("Could not close statement: %v", err)
	}
	err = txn.Commit()
	if err != nil {
		return fmt.Errorf("Could not commit transaction: %v", err)
	}

	return nil
}

func writeToDB(settings Settings, dbWriteQueue chan DataPoint) {
	// connect to the database
	db, err := connectDB(settings, true)
	if err != nil {
		if settings.Logging.Level >= 1 {
			log.Println("Cannot connect to database: ", err)
		}
		return
	}

	// make sure the table exists
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS traffic (
		id BIGSERIAL,
        time TIMESTAMPTZ NOT NULL,
        bandwidth REAL NOT NULL,
        iface TEXT,
        ingress BOOLEAN NOT NULL,
        remote_asn BIGINT,
		local_asn BIGINT,
		PRIMARY KEY(id, time)
    );`)
	if err != nil {
		// tables does not exist and can't be created
		if settings.Logging.Level >= 1 {
			log.Println("Error creating DB table \"traffic\": ", err)
		}
		return
	}

	// mainloop for this routine
	for {
		// read aggregated data from the channel
		point := <-dbWriteQueue
		for {
			// try to insert data, until it works, so we don't lose any rows
			if settings.Logging.Level >= 2 {
				log.Println("Bulk-inserting data points.")
			}
			err := bulkinsert(settings, db, &point)
			if err == nil {
				if settings.Logging.Level >= 2 {
					log.Println("Bulk-insert successful.")
				}
				// next row
				break
			} else {
				// something went wrong. log, reconnect, retry
				if settings.Logging.Level >= 1 {
					log.Printf("DB insert failed: ", err)
					log.Printf("Trying to reconnect...")
				}
				db, _ = connectDB(settings, true)
			}
		}
	}
}

func main() {
	// read the settings file
	settings, err := readSettings("settings.yaml")
	if settings.Logging.Level >= 3 {
		log.Printf("Debug: Settings: %v", settings)
	}

	// initialize logger
	if settings.Logging.File == "" {
		// only stdout
		log.SetOutput(os.Stdout)
		log.Println("No log filename defined. Logging only to stdout.")
	} else {
		// stdout + logfile
		logfile, err := os.OpenFile(settings.Logging.File, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			if settings.Logging.Level >= 1 {
				// no logging available here :-O
				fmt.Println("Error opening file for logging: ", err)
			}
			return
		}
		defer logfile.Close()
		mw := io.MultiWriter(os.Stdout, logfile)
		log.SetOutput(mw)
	}

	ifaces, err := readInterfaceConfig("interfaces.yaml")
	if err != nil {
		if settings.Logging.Level >= 1 {
			log.Printf("Error while parsing interface config: ", err)
		}
		return
	}
	if settings.Logging.Level >= 3 {
		log.Println("Debug: Interfaces:")
		for addr, iface := range ifaces {
			log.Printf("Debug: Interface %d: %v", addr, iface)
		}
	}

	// start accepting netflow
	nfs := nfserver.New(settings.Netflow.Address, settings.Netflow.Threads, false, settings.Logging.Level)
	flows := nfs.Output
	if settings.Logging.Level >= 1 {
		log.Println("Started tflow2 nfserver.")
	}

	// get current ts to know when a minute has elapsed
	old_ts := time.Now()

	// variables for influx
	var size uint64

	cache := make(map[TrafficAttributes]uint64)
	dbWriteQueue := make(chan DataPoint)

	go writeToDB(settings, dbWriteQueue)

	if settings.Logging.Level >= 1 {
		log.Println("Waiting for flows.")
	}
	// mainloop
	for {
		if ql := len(flows); ql > 1 {
			if settings.Logging.Level >= 2 {
				log.Println("Reading too slow, queue size is ", ql)
			}
		}
		flow := <-flows
		// TODO move this to a function?
		host := net.IP(flow.Router).String()

		for _, iface := range ifaces {
			// traffic can flow through more than one interface
			// --> match all interfaces in both directions
			if iface.Host == host {
				if iface.Id == flow.IntIn {
					size = flow.Size * iface.Sample * 8
					var attr TrafficAttributes
					attr.iface = iface
					attr.remoteASN = flow.SrcAs
					attr.localASN = flow.DstAs
					attr.ingress = true
					cache[attr] = cache[attr] + size
				}
				if iface.Id == flow.IntOut {
					size = flow.Size * iface.Sample * 8
					var attr TrafficAttributes
					attr.iface = iface
					attr.remoteASN = flow.DstAs
					attr.localASN = flow.SrcAs
					attr.ingress = false
					cache[attr] = cache[attr] + size
				}
			}
		}

		// check if a time has elapsed, flush to the database and restart count if it has
		if uint64(time.Since(old_ts)/time.Second) >= settings.Aggregation.Period {
			// TODO: FIXME: for small time periods, or few flows, this does not get called
			// often enough, and the resulting DB writes are unevenly timed
			if settings.Logging.Level >= 2 {
				log.Println("Writing a chunk of data points to the DB. Entries: ", len(cache))
			}
			// move the aggregated data to another goroutine; write it to the database
			point := DataPoint{}
			point.data = cache
			point.time = time.Now().UTC()
			dbWriteQueue <- point

			// get a new and empty cache
			cache = make(map[TrafficAttributes]uint64)

			// set the beginning of the next minute
			old_ts = time.Now()
		}
	}
}
