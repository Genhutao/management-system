package com.xgh.app.ui

import android.content.Intent
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import androidx.recyclerview.widget.LinearLayoutManager
import androidx.recyclerview.widget.RecyclerView
import com.xgh.app.R
import com.xgh.app.XghApp
import com.xgh.app.data.ApiClient
import com.xgh.app.data.SlotNoticeResponse
import com.xgh.app.data.TaskCard
import com.xgh.app.data.TodayTasksResponse
import com.xgh.app.databinding.ActivityHomeBinding
import com.xgh.app.databinding.ItemTaskBinding
import com.xgh.app.util.Ui
import kotlinx.coroutines.launch

class HomeActivity : BaseActivity() {

    private lateinit var binding: ActivityHomeBinding
    private val adapter = TaskAdapter()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityHomeBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.rvTasks.layoutManager = LinearLayoutManager(this)
        binding.rvTasks.adapter = adapter
        binding.refresh.setOnRefreshListener { refresh() }
        binding.fabReport.setOnClickListener { startActivity(Intent(this, UploadActivity::class.java)) }
        binding.btnHistory.setOnClickListener { startActivity(Intent(this, HistoryActivity::class.java)) }
        binding.btnSettings.setOnClickListener { startActivity(Intent(this, SettingsActivity::class.java)) }

        val app = application as XghApp
        lifecycleScope.launch {
            binding.tvHello.text = getString(R.string.home_greeting)
            val name = app.sessionStore.userName
            name.collect { n ->
                if (!n.isNullOrBlank()) binding.tvHello.text = "${n}，今日待办"
            }
        }
        refresh()
    }

    override fun onResume() {
        super.onResume()
        refresh()
    }

    private var refreshing = false
    private var refreshQueued = false

    private fun refresh() {
        // 防重入：在途时只记一次"待重跑"，结束后用新数据再刷一轮，
        // 避免从上报页返回时撞上旧请求、渲染回旧计数（如"已上报 0 条"）
        if (refreshing) {
            refreshQueued = true
            return
        }
        refreshing = true
        lifecycleScope.launch {
            try {
                binding.refresh.isRefreshing = true
                val tasks = Ui.request(this@HomeActivity) { ApiClient.get().service.todayTasks() }
                val notice = Ui.request(this@HomeActivity) { ApiClient.get().service.slotNotice() }
                renderSlot(notice)
                renderTasks(tasks)
            } finally {
                binding.refresh.isRefreshing = false
                refreshing = false
            }
            if (refreshQueued) {
                refreshQueued = false
                refresh()
            }
        }
    }

    private fun renderSlot(notice: SlotNoticeResponse?) {
        val active = notice?.active_slot
        val text = buildString {
            if (active?.is_active_now == true) {
                active.remaining_minutes?.let { append(getString(R.string.slot_remaining, it)) }
                append("  ")
            }
            active?.today_submitted_count?.let { append(getString(R.string.slot_done, it)) }
        }.trim()
        binding.tvSlot.text = text.ifEmpty { notice?.server_time ?: "" }
    }

    private fun renderTasks(resp: TodayTasksResponse?) {
        val cards = resp?.cards.orEmpty()
        binding.progress.visibility = View.GONE
        if (resp == null) {
            // 请求失败：给出明确的可操作提示，而不是伪装成"无待办"
            binding.tvEmpty.text = getString(R.string.net_error_retry)
            binding.tvEmpty.visibility = View.VISIBLE
            return
        }
        binding.tvEmpty.text = getString(R.string.empty_tasks)
        binding.tvEmpty.visibility = if (cards.isEmpty()) View.VISIBLE else View.GONE
        adapter.submit(cards)
    }

    private class TaskAdapter : RecyclerView.Adapter<TaskHolder>() {
        private val items = mutableListOf<TaskCard>()
        fun submit(list: List<TaskCard>) {
            items.clear(); items.addAll(list); notifyDataSetChanged()
        }
        override fun onCreateViewHolder(parent: ViewGroup, viewType: Int): TaskHolder =
            TaskHolder(ItemTaskBinding.inflate(LayoutInflater.from(parent.context), parent, false))
        override fun getItemCount() = items.size
        override fun onBindViewHolder(holder: TaskHolder, position: Int) =
            holder.bind(items[position])
    }

    private class TaskHolder(val b: ItemTaskBinding) : RecyclerView.ViewHolder(b.root) {
        fun bind(card: TaskCard) {
            b.tvTitle.text = card.title ?: ""
            b.tvPriority.text = when (card.priority) {
                "high" -> "高优先"
                "medium" -> "中优先"
                else -> "常规"
            }
            b.tvPeriod.text = listOfNotNull(card.period, card.building).joinToString(" · ")
            b.tvDuty.text = card.duty_members?.takeIf { it.isNotBlank() }
                ?.let { "当班：$it" } ?: ""
            val prompt = card.prompt_text?.takeIf { it.isNotBlank() }
            b.tvPrompt.visibility = if (prompt != null) View.VISIBLE else View.GONE
            b.tvPrompt.text = prompt
        }
    }
}
