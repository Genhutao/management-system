package com.xgh.app.ui

import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import coil.load
import com.google.android.material.chip.Chip
import com.xgh.app.R
import com.xgh.app.XghApp
import com.xgh.app.data.ApiClient
import com.xgh.app.data.InspectionPhoto
import com.xgh.app.data.InspectionsResponse
import com.xgh.app.databinding.ActivityHistoryBinding
import com.xgh.app.databinding.ItemDateHeaderBinding
import com.xgh.app.databinding.ItemInspectionBinding
import com.xgh.app.util.Ui
import kotlinx.coroutines.launch

/**
 * 历史上报列表：每行一条、按日期分节（节头含当日条数）、滚动后可一键返回顶部。
 * 数据源 GET /dorm/inspections（硬编码 Limit 50，按 building 隔离）。
 */
class HistoryActivity : AppCompatActivity() {

    private lateinit var binding: ActivityHistoryBinding
    private val adapter = HistoryAdapter()
    private var severity: String? = null

    /** 列表行：日期节头 或 记录 */
    private sealed class Row {
        data class Header(val date: String, val count: Int) : Row()
        data class Item(val photo: InspectionPhoto) : Row()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityHistoryBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.rvHistory.layoutManager = LinearLayoutManager(this)
        binding.rvHistory.adapter = adapter
        binding.refresh.setOnRefreshListener { refresh() }
        binding.btnBack.setOnClickListener { finish() }

        // 滑过约 2 屏出现"回顶"，点击平滑回到顶部
        binding.rvHistory.addOnScrollListener(object : RecyclerView.OnScrollListener() {
            override fun onScrolled(rv: RecyclerView, dx: Int, dy: Int) {
                val lm = rv.layoutManager as? LinearLayoutManager ?: return
                binding.btnTop.visibility =
                    if (lm.findFirstVisibleItemPosition() > 6) View.VISIBLE else View.GONE
            }
        })
        binding.btnTop.setOnClickListener {
            binding.rvHistory.smoothScrollToPosition(0)
        }

        setupSeverityChips()
        refresh()
    }

    private fun setupSeverityChips() {
        val options = listOf(
            "" to getString(R.string.filter_all),
            "high" to getString(R.string.filter_severity_high),
            "medium" to getString(R.string.filter_severity_medium),
            "low" to getString(R.string.filter_severity_low)
        )
        options.forEachIndexed { index, (value, label) ->
            val chip = Chip(this)
            chip.text = label
            chip.isCheckable = true
            chip.id = View.generateViewId()
            chip.setOnClickListener {
                severity = value.ifEmpty { null }
                refresh()
            }
            binding.chipSeverity.addView(chip)
            if (index == 0) chip.isChecked = true
        }
    }

    private fun refresh() {
        lifecycleScope.launch {
            binding.refresh.isRefreshing = true
            val resp = Ui.request(this@HistoryActivity) {
                ApiClient.get().service.inspections(null, severity)
            } as InspectionsResponse?
            binding.refresh.isRefreshing = false
            binding.progress.visibility = View.GONE
            val items = resp?.items.orEmpty()
            binding.tvEmpty.visibility = if (items.isEmpty()) View.VISIBLE else View.GONE
            binding.btnTop.visibility = View.GONE
            adapter.submit(groupByDate(items))
        }
    }

    /** 按日期（created_at 前 10 位）分组，接口本身按时间倒序返回 */
    private fun groupByDate(items: List<InspectionPhoto>): List<Row> {
        val rows = mutableListOf<Row>()
        var currentDate: String? = null
        for (item in items) {
            val date = item.created_at?.take(10) ?: "未知日期"
            if (date != currentDate) {
                currentDate = date
                rows.add(Row.Header(date, items.count { it.created_at?.take(10) == date }))
            }
            rows.add(Row.Item(item))
        }
        return rows
    }

    private inner class HistoryAdapter : RecyclerView.Adapter<RecyclerView.ViewHolder>() {
        private val rows = mutableListOf<Row>()

        fun submit(list: List<Row>) {
            rows.clear(); rows.addAll(list); notifyDataSetChanged()
        }

        override fun getItemViewType(position: Int) =
            if (rows[position] is Row.Header) 0 else 1

        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): RecyclerView.ViewHolder =
            if (viewType == 0) {
                HeaderHolder(ItemDateHeaderBinding.inflate(LayoutInflater.from(parent.context), parent, false))
            } else {
                ItemHolder(ItemInspectionBinding.inflate(LayoutInflater.from(parent.context), parent, false))
            }

        override fun getItemCount() = rows.size

        override fun onBindViewHolder(holder: RecyclerView.ViewHolder, position: Int) {
            when (val row = rows[position]) {
                is Row.Header -> (holder as HeaderHolder).bind(row)
                is Row.Item -> (holder as ItemHolder).bind(row.photo)
            }
        }
    }

    private class HeaderHolder(val b: ItemDateHeaderBinding) : RecyclerView.ViewHolder(b.root) {
        fun bind(header: Row.Header) {
            b.tvDate.text = header.date
            b.tvCount.text = "${header.count} 条"
        }
    }

    private inner class ItemHolder(val b: ItemInspectionBinding) : RecyclerView.ViewHolder(b.root) {

        fun bind(item: InspectionPhoto) {
            b.tvRoom.text = "${item.building ?: ""} ${item.room_number ?: ""}"
            val (label, colorRes) = when (item.severity) {
                "high" -> "严重" to R.color.error
                "medium" -> "中等" to R.color.warning
                "low" -> "轻微" to R.color.success
                else -> "待核对" to R.color.info_neutral
            }
            b.tvSeverity.text = label
            b.tvSeverity.background.mutate().setTint(
                androidx.core.content.ContextCompat.getColor(b.root.context, colorRes)
            )
            b.tvMeta.text = listOfNotNull(
                item.manager_name?.let { "宿管：$it" },
                item.created_at?.take(16)?.replace('T', ' ')
            ).joinToString(" · ")
            val note = item.note_text?.takeIf { it.isNotBlank() }
            b.tvNote.visibility = if (note != null) View.VISIBLE else View.GONE
            b.tvNote.text = note

            val app = application as XghApp
            lifecycleScope.launch {
                val url = app.sessionStore.imageUrl(item.image_url)
                if (url != null) b.ivPhoto.load(url) { crossfade(true) }
            }
        }
    }
}
