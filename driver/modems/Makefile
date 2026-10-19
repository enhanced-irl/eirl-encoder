# SPDX-License-Identifier: GPL-2.0-only
# Cellular modem drivers L4T R32.7.6 does not build (qmi_wwan, cdc_mbim, huawei_cdc_ncm), option
# with the Quectel IDs it lacks, and ipheth (an iPhone's USB tethering). Sources are the kernel's own,
# patched by build/build.sh (MODEMS.md §4).
obj-m := qmi_wwan.o cdc_mbim.o huawei_cdc_ncm.o option.o ipheth.o
