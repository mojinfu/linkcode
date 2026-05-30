import codecs

with codecs.open('internal/router/handler.go', 'r', 'utf-8') as f:
    content = f.read()

# 1. Add hadContent = true before cache.textBuf.WriteString in KindText case
content = content.replace(
    'case agent.KindText:\n\t\t\t\tcache.textBuf.WriteString(chunk.Content)',
    'case agent.KindText:\n\t\t\t\thadContent = true\n\t\t\t\tcache.textBuf.WriteString(chunk.Content)'
)

# 2. Add hadContent = true in KindFinal case
content = content.replace(
    'case agent.KindFinal:\n\t\t\t\tcache.fullResponse = chunk.Content',
    'case agent.KindFinal:\n\t\t\t\thadContent = true\n\t\t\t\tcache.fullResponse = chunk.Content'
)

# 3. Add processTimeout reset after goto done block (before spinnerDotIdx++)
content = content.replace(
    '\t\t\t\tgoto done\n\t\t\t}\n\t\t\tspinnerDotIdx++',
    '\t\t\t\tgoto done\n\t\t\t}\n\t\t\tprocessTimeout = time.After(10 * time.Minute)\n\t\t\tspinnerDotIdx++'
)

# 4. Add processTimeout case in select, before the ticker case
old_ticker = '\t\t\tcase <-ticker.C:'
new_timeout = '\t\t\tcase <-processTimeout:\n\t\t\t\tif streamBroken {\n\t\t\t\t\tcontinue\n\t\t\t\t}\n\t\t\t\tr.sendStreamReply(msg, r.styler.Bar("[⏰] 超时")+"\\nAgent 长时间未响应，已终止", streamID, true)\n\t\t\t\treturn\n\n\t\t\tcase <-ticker.C:'
content = content.replace(old_ticker, new_timeout)

# 5. Add silent close detection in done: block
# The done block has this pattern:
# done:
#		r.mu.Lock()
#		interrupted := ...
#		delete(...)
#		r.mu.Unlock()
#		if interrupted {
#			...
#			return
#		}
#
#		responseText := ...
#
# We want to add the check after the interrupted check:
old_done_check = '\t\tif interrupted {\n\t\t\tstopText := r.styler.Bar'
new_done_check = '\t\tif interrupted {\n\t\t\tstopText := r.styler.Bar'
# Find the end of the interrupted block (the 'return' and '}' after 'if interrupted')
# Then add our check before responseText

# Find the pattern: after interrupted block ends with '}\n\n\t\tresponseText'
old_resp = '\t\t}\n\n\t\tresponseText := cache.textBuf.String()'
new_resp = '\t\t}\n\n\t\tif !hadContent {\n\t\t\tr.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})\n\t\t\tdoneText := r.styler.Bar("[💫] 无响应") + "\\nAgent 进程异常退出，请稍后重试"\n\t\t\tr.sendStreamReply(msg, doneText, streamID, true)\n\t\t\treturn\n\t\t}\n\n\t\tresponseText := cache.textBuf.String()'
content = content.replace(old_resp, new_resp)

with codecs.open('internal/router/handler.go', 'w', 'utf-8') as f:
    f.write(content)

print('done')
