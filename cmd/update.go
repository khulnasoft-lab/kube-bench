package cmd

import (
	"fmt"

	"github.com/golang/glog"
	"github.com/khulnasoft-lab/kube-bench/internal/updater"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	RootCmd.AddCommand(updateCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the cfg directory from the upstream source",
	Long:  "Download the latest configuration bundle from the configured source and replace the local cfg/ directory.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Read values via Viper to support env var overrides
		src := viper.GetString("update-source")
		ref := viper.GetString("update-ref")
		sum := viper.GetString("update-checksum")

		glog.V(1).Infof("Updating configuration bundle from %s@%s", src, ref)
		if err := updater.Update(cmd.Context(), updater.Options{
			Source:        src,
			Ref:           ref,
			TargetCfgDir:  cfgDir,
			BackupEnabled: true,
			Checksum:      sum,
		}); err != nil {
			return fmt.Errorf("config update failed: %w", err)
		}
		glog.Infof("Configuration updated in %s", cfgDir)
		return nil
	},
}
